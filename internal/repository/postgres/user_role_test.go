package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
)

// A freshly-created librarian has no category. This is the production failure:
// changing only role used to violate members_need_a_category and become HTTP 500.
func TestLibrarianDemotionNeedsCategory(t *testing.T) {
	db := discoveryDB(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := db.Exec(ctx, `INSERT INTO users(id,identifier,email,full_name,password_hash,role) VALUES($1,$2,$3,'Role regression','x','librarian')`, id, id.String(), id.String()+"@role.invalid"); err != nil {
		t.Fatal(err)
	}
	err := NewUserRepo(db).UpdateRole(ctx, id, domain.RoleMember, nil, uuid.Nil)
	if !errors.Is(err, domain.ErrNoCategory) {
		t.Fatalf("want a clear missing-category error, got %v", err)
	}
}

func TestRoleChangeCategoryAndAuditAreAtomic(t *testing.T) {
	db := discoveryDB(t)
	ctx := context.Background()
	repo := NewUserRepo(db)
	create := func(role domain.Role, category *domain.MemberCategory) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := db.Exec(ctx, `INSERT INTO users(id,identifier,email,full_name,password_hash,role,category) VALUES($1,$2,$3,'Role regression','x',$4,$5)`, id, id.String(), id.String()+"@role.invalid", role, category); err != nil {
			t.Fatal(err)
		}
		return id
	}
	admin := create(domain.RoleAdmin, nil)
	for _, category := range []domain.MemberCategory{domain.CategoryUndergraduate, domain.CategoryPostgraduate, domain.CategoryStaff} {
		t.Run(string(category), func(t *testing.T) {
			id := create(domain.RoleLibrarian, nil)
			before, err := repo.TokensInvalidBefore(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			// Failure must leave authorization, category, sessions and audit unchanged.
			if err := repo.UpdateRole(ctx, id, domain.RoleMember, nil, admin); !errors.Is(err, domain.ErrNoCategory) {
				t.Fatal(err)
			}
			u, err := repo.FindByID(ctx, id)
			if err != nil || u.Role != domain.RoleLibrarian || u.Category != nil {
				t.Fatalf("failed demotion altered user: %+v %v", u, err)
			}
			unchanged, err := repo.TokensInvalidBefore(ctx, id)
			if err != nil || !unchanged.Equal(before) {
				t.Fatalf("failed demotion revoked session: %v", err)
			}
			var n int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1`, id).Scan(&n); err != nil || n != 0 {
				t.Fatalf("failed demotion audit=%d %v", n, err)
			}
			if err := repo.UpdateRole(ctx, id, domain.RoleMember, &category, admin); err != nil {
				t.Fatal(err)
			}
			u, err = repo.FindByID(ctx, id)
			if err != nil || u.Role != domain.RoleMember || u.Category == nil || *u.Category != category {
				t.Fatalf("member: %+v %v", u, err)
			}
			// JWT timestamps are whole seconds: even a token minted in this
			// second must lose its obsolete librarian claims immediately.
			valid, err := repo.SessionValid(ctx, id, time.Now().Truncate(time.Second), "librarian")
			if err != nil || valid {
				t.Fatalf("stale librarian token survived demotion: %v", err)
			}
			valid, err = repo.SessionValid(ctx, id, time.Now().Truncate(time.Second), "member")
			if err != nil || !valid {
				t.Fatalf("fresh member session rejected: %v", err)
			}
			invalidBefore, err := repo.TokensInvalidBefore(ctx, id)
			if err != nil || !invalidBefore.After(before) {
				t.Fatalf("role change did not invalidate old sessions: %v", err)
			}
			var from, to, stored string
			if err := db.QueryRow(ctx, `SELECT metadata->>'from',metadata->>'to',metadata->>'category_to' FROM audit_log WHERE entity_id=$1 AND action='MEMBER_ROLE_CHANGED'`, id).Scan(&from, &to, &stored); err != nil || from != "librarian" || to != "member" || stored != string(category) {
				t.Fatalf("audit: %s %s %s %v", from, to, stored, err)
			}
			if err := repo.UpdateRole(ctx, id, domain.RoleLibrarian, nil, admin); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateRole(ctx, id, domain.RoleMember, nil, admin); err != nil {
				t.Fatal(err)
			}
			u, err = repo.FindByID(ctx, id)
			if err != nil || u.Category == nil || *u.Category != category {
				t.Fatalf("lost existing category: %+v %v", u, err)
			}
			// A same-role request with a new category is not a no-op.
			replacement := domain.CategoryStaff
			if err := repo.UpdateRole(ctx, id, domain.RoleMember, &replacement, admin); err != nil {
				t.Fatal(err)
			}
			u, err = repo.FindByID(ctx, id)
			if err != nil || u.Category == nil || *u.Category != replacement {
				t.Fatalf("category-only change: %+v %v", u, err)
			}
		})
	}
	category := domain.CategoryUndergraduate
	if err := repo.UpdateRole(ctx, admin, domain.RoleMember, &category, admin); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last admin demotion: %v", err)
	}
	id := create(domain.RoleLibrarian, nil)
	if err := repo.UpdateRole(ctx, id, domain.RoleMember, &category, uuid.New()); err == nil {
		t.Fatal("expected audit foreign-key failure")
	}
	u, err := repo.FindByID(ctx, id)
	if err != nil || u.Role != domain.RoleLibrarian || u.Category != nil {
		t.Fatalf("audit failure did not roll back: %+v %v", u, err)
	}
	if err := repo.UpdateRole(ctx, uuid.New(), domain.RoleMember, &category, admin); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing member: %v", err)
	}
}

func TestConcurrentAdministratorDemotionsPreserveOneAdmin(t *testing.T) {
	db := discoveryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := NewUserRepo(db)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range ids {
		if _, err := db.Exec(ctx, `INSERT INTO users(id,identifier,email,full_name,password_hash,role) VALUES($1,$2,$3,'Admin regression','x','admin')`, id, id.String(), id.String()+"@role.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range ids {
		go func(id uuid.UUID) {
			<-start
			category := domain.CategoryStaff
			results <- repo.UpdateRole(ctx, id, domain.RoleMember, &category, uuid.Nil)
		}(id)
	}
	close(start)
	success, conflicts := 0, 0
	for range ids {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent demotion: %v", err)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if success != 1 || conflicts != 1 || count != 1 {
		t.Fatalf("success %d conflicts %d admins %d", success, conflicts, count)
	}
}

func TestRoleRevocationTimestampFollowsLockWait(t *testing.T) {
	db := discoveryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, target := uuid.New(), uuid.New()
	for _, entry := range []struct {
		id   uuid.UUID
		role string
	}{{admin, "admin"}, {target, "librarian"}} {
		if _, err := db.Exec(ctx, `INSERT INTO users(id,identifier,email,full_name,password_hash,role) VALUES($1,$2,$3,'Role wait regression','x',$4)`, entry.id, entry.id.String(), entry.id.String()+"@role.invalid", entry.role); err != nil {
			t.Fatal(err)
		}
	}
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, admin); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		category := domain.CategoryStaff
		done <- NewUserRepo(db).UpdateRole(ctx, target, domain.RoleMember, &category, admin)
	}()
	// Observe the blocked statement rather than relying on a scheduler delay.
	for {
		var waiting bool
		err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query=$1)`, "SELECT id FROM users WHERE role = 'admin' ORDER BY id FOR UPDATE").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("role change did not wait for lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var duringWait time.Time
	if err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&duringWait); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cutoff, err := NewUserRepo(db).TokensInvalidBefore(ctx, target)
	if err != nil || !cutoff.After(duringWait) {
		t.Fatalf("cutoff %v predates lock release (%v): %v", cutoff, duringWait, err)
	}
}
