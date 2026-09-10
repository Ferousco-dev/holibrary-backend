package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
)

func editPtr[T any](v T) *T { return &v }

func TestBookMetadataUpdatePostgres(t *testing.T) {
	db := discoveryDB(t)
	ctx := context.Background()
	repo := NewCatalogueRepo(db)
	var actor uuid.UUID
	if err := db.QueryRow(ctx, `INSERT INTO users(identifier,email,full_name,password_hash,role,category) VALUES('editor','editor@example.invalid','Editor','unused','librarian',NULL) RETURNING id`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	book, err := repo.CreateBook(ctx, CreateBookParams{Title: "Original", CallNumber: "QA 100", Authors: []string{"Old author"}, Subjects: []string{"Old subject"}, Publisher: "Original publisher", StaffID: actor})
	if err != nil {
		t.Fatal(err)
	}
	copy, err := repo.AddCopy(ctx, book.ID, "EDIT-001", domain.PolicyCirculating, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO loans(copy_id,user_id,due_at,issued_by) VALUES($1,$2,now()+interval '7 days',$2)`, copy.ID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO reservations(book_id,user_id) VALUES($1,$2)`, book.ID, actor); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var result string
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
			t.Fatal(err)
		}
		err = tx.QueryRow(ctx, `SELECT jsonb_build_object('copies',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM copies c),'loans',(SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM loans l),'reservations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM reservations r))::text`).Scan(&result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	circulation := snapshot()
	if _, err := db.Exec(ctx, `UPDATE books SET updated_at='2000-01-01' WHERE id=$1`, book.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.UpdateBook(ctx, book.ID, UpdateBookParams{Title: editPtr("Updated algorithms"), CallNumber: editPtr("D 100"), Authors: editPtr([]string{"Zelda", "Ada"}), Subjects: editPtr([]string{"Zoology", "Computation"}), PublishedYear: editPtr(1000), StaffID: actor})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Updated algorithms" || updated.LCCClass != "D" || domain.WingFor(updated.LCCClass[0]) != domain.WingSouth || updated.Publisher != book.Publisher || updated.ID != book.ID || !updated.CreatedAt.Equal(book.CreatedAt) || !slices.Equal(updated.Authors, []string{"Zelda", "Ada"}) {
		t.Fatalf("incorrect partial update: %+v", updated)
	}
	if snapshot() != circulation {
		t.Fatal("metadata editing changed circulation records")
	}
	for _, query := range []string{"Computation", "Zelda", "algorithms"} {
		found, total, err := repo.Search(ctx, SearchParams{Query: query, Limit: 20})
		if err != nil || total != 1 || len(found) != 1 || found[0].ID != book.ID {
			t.Fatalf("search %s total=%d result=%+v err=%v", query, total, found, err)
		}
	}
	var loggedActor uuid.UUID
	var entityType string
	var metadata []byte
	var loggedAt, updatedAt time.Time
	if err := db.QueryRow(ctx, `SELECT actor_id,entity_type,metadata,created_at FROM audit_log WHERE action='BOOK_METADATA_UPDATED' AND entity_id=$1`, book.ID).Scan(&loggedActor, &entityType, &metadata, &loggedAt); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Changed []string `json:"changed_fields"`
	}
	if err := json.Unmarshal(metadata, &audit); err != nil {
		t.Fatal(err)
	}
	if loggedActor != actor || entityType != "book" || !slices.Equal(audit.Changed, []string{"authors", "call_number", "published_year", "subjects", "title"}) {
		t.Fatalf("audit actor=%s entity=%s metadata=%s", loggedActor, entityType, metadata)
	}
	if err := db.QueryRow(ctx, `SELECT updated_at FROM books WHERE id=$1`, book.ID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if !updatedAt.Equal(loggedAt) {
		t.Fatalf("audit timestamp %v differs from update %v", loggedAt, updatedAt)
	}
	t.Run("identical patch is no-op including subject order", func(t *testing.T) {
		if _, err := repo.UpdateBook(ctx, book.ID, UpdateBookParams{Title: &updated.Title, Subjects: editPtr([]string{"Zoology", "Computation"}), StaffID: actor}); err != nil {
			t.Fatal(err)
		}
		var count int
		var stamp time.Time
		if err := db.QueryRow(ctx, `SELECT updated_at,(SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='BOOK_METADATA_UPDATED') FROM books WHERE id=$1`, book.ID).Scan(&stamp, &count); err != nil {
			t.Fatal(err)
		}
		if count != 1 || !stamp.Equal(updatedAt) {
			t.Fatalf("no-op changed audit/timestamp count=%d stamp=%v", count, stamp)
		}
	})
	t.Run("duplicate ISBN rolls back whole update", func(t *testing.T) {
		isbn := "9780262033848"
		if _, err := repo.CreateBook(ctx, CreateBookParams{Title: "Other", CallNumber: "QA 200", ISBN13: isbn, StaffID: actor}); err != nil {
			t.Fatal(err)
		}
		_, err := repo.UpdateBook(ctx, book.ID, UpdateBookParams{Title: editPtr("Should rollback"), ISBN13: &isbn, Authors: editPtr([]string{"Replacement"}), StaffID: actor})
		if !errors.Is(err, domain.ErrDuplicateISBN) {
			t.Fatalf("duplicate err=%v", err)
		}
		after, err := repo.FindBook(ctx, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, updated) {
			t.Fatalf("duplicate changed book: %+v", after)
		}
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='BOOK_METADATA_UPDATED'`, book.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("failed update audited: %d", count)
		}
	})
	t.Run("audit failure rolls back metadata and access points", func(t *testing.T) {
		_, err := repo.UpdateBook(ctx, book.ID, UpdateBookParams{Title: editPtr("Should rollback"), Authors: editPtr([]string{"Replacement"}), Subjects: editPtr([]string{"Replacement"}), StaffID: uuid.New()})
		if err == nil {
			t.Fatal("invalid audit actor accepted")
		}
		after, err := repo.FindBook(ctx, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, updated) {
			t.Fatalf("audit failure changed book: %+v", after)
		}
	})
	t.Run("missing book", func(t *testing.T) {
		_, err := repo.UpdateBook(ctx, uuid.New(), UpdateBookParams{Title: editPtr("Missing"), StaffID: actor})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("concurrent disjoint patches preserve both changes", func(t *testing.T) {
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, patch := range []UpdateBookParams{{Title: editPtr("Concurrent title"), StaffID: actor}, {Publisher: editPtr("Concurrent publisher"), StaffID: actor}} {
			wg.Add(1)
			go func(p UpdateBookParams) {
				defer wg.Done()
				<-start
				_, err := repo.UpdateBook(ctx, book.ID, p)
				errs <- err
			}(patch)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		after, err := repo.FindBook(ctx, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Title != "Concurrent title" || after.Publisher != "Concurrent publisher" {
			t.Fatalf("lost concurrent edit: %+v", after)
		}
		if snapshot() != circulation {
			t.Fatalf("concurrent metadata editing changed circulation: before=%s after=%s", circulation, snapshot())
		}
	})
	t.Run("explicit empty values clear metadata and links", func(t *testing.T) {
		after, err := repo.UpdateBook(ctx, book.ID, UpdateBookParams{Publisher: editPtr(""), Authors: editPtr([]string{}), Subjects: editPtr([]string{}), StaffID: actor})
		if err != nil {
			t.Fatal(err)
		}
		if after.Publisher != "" || len(after.Authors) != 0 || len(after.Subjects) != 0 {
			t.Fatalf("failed clear: %+v", after)
		}
	})
}
