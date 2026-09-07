package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func TestSavedSearchOwnershipAndConcurrentLimit(t *testing.T) {
	ctx := context.Background()
	db := discoveryDB(t)
	var err error
	owner, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, other} {
		_, err = db.Exec(ctx, `INSERT INTO users(id,identifier,email,full_name,password_hash,role,category) VALUES($1,$2,$3,'Saved search test','x','member','undergraduate')`, id, "saved-"+id.String(), id.String()+"@saved.invalid")
		if err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(ctx, `DELETE FROM audit_log WHERE actor_id IN ($1,$2)`, owner, other)
	defer db.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, owner, other)
	repo := NewSavedSearchRepo(db)
	query := map[string]json.RawMessage{"q": json.RawMessage(`"private phrase"`)}
	var wg sync.WaitGroup
	results := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := repo.Create(ctx, owner, "Books", query, 20); results <- err }()
	}
	wg.Wait()
	close(results)
	successes, limited := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, domain.ErrSavedSearchLimit) {
			limited++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 20 || limited != 10 {
		t.Fatalf("successes %d limited %d", successes, limited)
	}
	own, err := repo.ListForUser(ctx, owner)
	if err != nil || len(own) != 20 {
		t.Fatalf("owner list %d %v", len(own), err)
	}
	foreign, err := repo.ListForUser(ctx, other)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("other list %d %v", len(foreign), err)
	}
	if err = repo.Delete(ctx, own[0].ID, other); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign delete", err)
	}
	var notified bool
	var version int
	var checked any
	if err = db.QueryRow(ctx, `SELECT notifications_enabled,query_version,last_checked_at FROM saved_searches WHERE id=$1`, own[0].ID).Scan(&notified, &version, &checked); err != nil {
		t.Fatal(err)
	}
	if notified || version != 1 || checked != nil {
		t.Fatal("notification state not disabled")
	}
	if err = repo.Delete(ctx, own[0].ID, owner); err != nil {
		t.Fatal(err)
	}
	var auditCount, privateCount int
	if err = db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE metadata::text LIKE '%private phrase%') FROM audit_log WHERE actor_id=$1 AND entity_type='saved_search'`, owner).Scan(&auditCount, &privateCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 21 || privateCount != 0 {
		t.Fatalf("audit count %d private %d", auditCount, privateCount)
	}
}
