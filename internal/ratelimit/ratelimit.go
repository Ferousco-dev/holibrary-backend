// Package ratelimit throttles abusive traffic.
//
// Two things are limited, for different reasons:
//
//   - Per ACCOUNT, to make password guessing impractical. This is the control
//     that actually matters, and it is the one an attacker cannot dodge: they
//     may change IP freely, but the account they are trying to break into is
//     fixed.
//
//   - Per CLIENT IP, to stop one source flooding the endpoint. This is coarse
//     and set generously, because a whole university sits behind a small number
//     of NAT addresses and a tight IP limit would lock out honest students
//     rather than attackers.
//
// The earlier implementation had only the second, at five attempts a minute,
// which locked out a shared campus egress while doing nothing against an
// attacker who could vary their apparent address. DEF-019.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter counts events in a fixed window.
type Limiter interface {
	// Allow records one event against key and reports whether it is within the
	// limit. An error means the limiter itself failed.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// ---------------------------------------------------------------- Redis

// Redis holds the counters outside the process.
//
// This is what makes the limit hold across a restart and across instances. An
// in-process counter resets every deploy, which is a gift to anyone patient
// enough to wait for one.
type Redis struct {
	client *redis.Client
	// prefix namespaces every key this application writes.
	//
	// A free Upstash account allows one database, so this one may be shared
	// with another application. Namespacing means the two cannot collide, and
	// it means every key belonging to the library can be found, inspected or
	// removed as a group. Sharing a Redis without a prefix is how one
	// application's cleanup silently becomes another's outage.
	prefix string
}

func NewRedis(url, prefix string) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		prefix = "holibrary"
	}
	return &Redis{client: redis.NewClient(opts), prefix: prefix + ":"}, nil
}

func (r *Redis) Ping(ctx context.Context) error { return r.client.Ping(ctx).Err() }

func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	// INCR then EXPIRE in one round trip. The expiry is set on every call
	// rather than only on the first: setting it only when the counter is 1
	// races two simultaneous first requests and can leave a key with no TTL,
	// which would ban the account permanently.
	key = r.prefix + key

	pipe := r.client.TxPipeline()
	count := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return count.Val() <= int64(limit), nil
}

func (r *Redis) Close() error { return r.client.Close() }

// ---------------------------------------------------------------- in-memory

// Memory is the fallback when Redis is unavailable.
//
// It is honest about its limits: single process, lost on restart. It exists so
// that a development machine without Redis still exercises the same code path,
// and so that a Redis outage degrades the limiter rather than the service.
type Memory struct {
	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	count int
	reset time.Time
}

func NewMemory() *Memory {
	m := &Memory{windows: make(map[string]*window)}
	go m.sweep()
	return m
}

func (m *Memory) Allow(_ context.Context, key string, limit int, w time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	entry, ok := m.windows[key]
	if !ok || now.After(entry.reset) {
		entry = &window{reset: now.Add(w)}
		m.windows[key] = entry
	}
	entry.count++
	return entry.count <= limit, nil
}

// sweep discards expired windows. Without it the map grows once per distinct
// key forever, which is itself a denial-of-service vector.
func (m *Memory) sweep() {
	for range time.Tick(time.Minute) {
		m.mu.Lock()
		now := time.Now()
		for k, w := range m.windows {
			if now.After(w.reset) {
				delete(m.windows, k)
			}
		}
		m.mu.Unlock()
	}
}

// ---------------------------------------------------------------- policy

// Policy is one named limit.
type Policy struct {
	Limit  int
	Window time.Duration
}

// The limits. Per-account is tight because it is precise; per-IP is generous
// because it is shared by everyone behind a campus NAT.
var (
	// Five attempts a minute against one account. An attacker cannot escape
	// this by changing address: the target account is the key.
	PerAccountLogin = Policy{Limit: 5, Window: time.Minute}

	// Three reset requests an hour per address, so the endpoint cannot be used
	// to flood a member's inbox.
	PerAccountReset = Policy{Limit: 3, Window: time.Hour}

	// Generous, because a faculty of students shares an egress address. This
	// catches a flood, not a guesser.
	PerIPAuth = Policy{Limit: 120, Window: time.Minute}
)
