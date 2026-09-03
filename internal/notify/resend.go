package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Resend sends email through the Resend API.
//
// Resend will only deliver to arbitrary addresses once a sending domain has
// been verified by DNS. Until then it accepts messages to the account owner
// only, so password reset will not reach students in production (RSK-002).
type Resend struct {
	apiKey string
	from   string
	client *http.Client
}

func NewResend(apiKey, from string) *Resend {
	return &Resend{
		apiKey: apiKey,
		from:   from,
		// A mail provider that hangs must not hold a worker slot open. The
		// worker retries, so a timeout costs one attempt, not a message.
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (r *Resend) Channel() string { return "email" }

// Configured reports whether an API key is present. Without one the worker
// leaves email queued rather than marking it failed, so nothing is lost while
// the account is being set up.
func (r *Resend) Configured() bool { return r.apiKey != "" }

func (r *Resend) Send(ctx context.Context, m Message) error {
	rendered := Render(m)

	body, err := json.Marshal(map[string]any{
		"from":    r.from,
		"to":      []string{m.To},
		"subject": rendered.Subject,
		"text":    rendered.Body,
	})
	if err != nil {
		return fmt.Errorf("%w: encoding message: %v", ErrPermanent, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		// A network failure is worth retrying; the provider may simply be busy.
		return fmt.Errorf("calling Resend: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return describe("Resend", resp.StatusCode, string(payload))
	}
	return nil
}
