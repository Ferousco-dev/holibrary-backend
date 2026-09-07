package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/migrate"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST_DATABASE_URL is deliberately separate from deployment DATABASE_URL.
// Every run owns a random schema, and never changes existing catalogue records.
func discoveryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set (isolated Postgres integration test)")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "catalogue_test_" + uuid.New().String()[:8]
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); admin.Close() })
	if _, err := migrate.Apply(ctx, db, false); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCatalogueDiscoveryPostgres(t *testing.T) {
	db := discoveryDB(t)
	ctx := context.Background()
	repo := NewCatalogueRepo(db)
	makeBook := func(title string, subjects, authors []string, year int) domain.Book {
		t.Helper()
		b, err := repo.CreateBook(ctx, CreateBookParams{Title: title, CallNumber: "QA 100", Subjects: subjects, Authors: authors, PublishedYear: &year, Edition: "Second", Language: "English", Faculty: "Science", Department: "Computing"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	source := makeBook("Source", []string{"Computing", "Algorithms"}, []string{"Ada"}, 2020)
	both := makeBook("Both subjects", []string{"Computing", "Algorithms"}, []string{"Other"}, 2024)
	subject := makeBook("One subject", []string{"Computing"}, []string{"Other"}, 2010)
	author := makeBook("Author only", []string{"History"}, []string{"Ada"}, 2025)
	archived := makeBook("Archived", []string{"Computing", "Algorithms"}, []string{"Ada"}, 2026)
	if err := repo.ArchiveBook(ctx, archived.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"available", "on_loan"} {
		if _, err := db.Exec(ctx, `INSERT INTO copies (book_id,accession_number,loan_policy,status) VALUES ($1,$2,'circulating',$3)`, both.ID, uuid.NewString(), status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.AddCopy(ctx, subject.ID, uuid.NewString(), domain.PolicyCirculating, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddCopy(ctx, author.ID, uuid.NewString(), domain.PolicyReferenceOnly, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	t.Run("metadata and final-copy retention", func(t *testing.T) {
		b, err := repo.FindBook(ctx, both.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b.Edition != "Second" || b.Language != "English" || b.CreatedAt.IsZero() {
			t.Fatalf("missing metadata: %+v", b)
		}
		if b.Availability.Available != 1 || b.Availability.Borrowable() != 0 || b.Availability.IsAvailable() {
			t.Fatalf("retained copy advertised as available: %+v", b.Availability)
		}
		yes, no := true, false
		for _, p := range []SearchParams{{Available: &yes}, {Borrowable: &yes}, {OnlyAvailable: true}} {
			p.Limit = 20
			books, total, err := repo.Search(ctx, p)
			if err != nil || total != 1 || len(books) != 1 || books[0].ID != subject.ID {
				t.Fatalf("available filter: books=%v total=%d err=%v", books, total, err)
			}
		}
		books, total, err := repo.Search(ctx, SearchParams{Available: &no, Limit: 20})
		if err != nil || total != 3 || len(books) != 3 {
			t.Fatalf("false filter: total=%d len=%d err=%v", total, len(books), err)
		}
		books, total, err = repo.Search(ctx, SearchParams{Available: &no, Borrowable: &yes, Limit: 20})
		if err != nil || total != 0 || len(books) != 0 {
			t.Fatalf("contradictory filters: total=%d err=%v", total, err)
		}
	})
	t.Run("filter intersection pagination and injection", func(t *testing.T) {
		year := 2015
		p := SearchParams{Subject: "Computing", Language: "english", Faculty: "science", Department: "computing", Wing: "North", YearFrom: &year, Limit: 1, Sort: "newest"}
		books, total, err := repo.Search(ctx, p)
		if err != nil || total != 2 || len(books) != 1 || books[0].ID != both.ID {
			t.Fatalf("first page: %+v total=%d err=%v", books, total, err)
		}
		p.Offset = 1
		books, total, err = repo.Search(ctx, p)
		if err != nil || total != 2 || len(books) != 1 || books[0].ID != source.ID {
			t.Fatalf("second page: %+v total=%d err=%v", books, total, err)
		}
		p.Offset = 99
		books, total, err = repo.Search(ctx, p)
		if err != nil || total != 2 || len(books) != 0 {
			t.Fatalf("out of range total=%d len=%d err=%v", total, len(books), err)
		}
		books, total, err = repo.Search(ctx, SearchParams{Author: "' OR TRUE --", Limit: 20})
		if err != nil || total != 0 || len(books) != 0 {
			t.Fatalf("injection altered filtering: total=%d err=%v", total, err)
		}
	})
	t.Run("related subjects before authors and archive exclusion", func(t *testing.T) {
		books, err := repo.Related(ctx, source.ID, 6)
		if err != nil || len(books) != 3 {
			t.Fatalf("related len=%d err=%v", len(books), err)
		}
		for i, want := range []uuid.UUID{both.ID, subject.ID, author.ID} {
			if books[i].ID != want {
				t.Fatalf("related rank %d got %s want %s", i, books[i].ID, want)
			}
		}
		books, err = repo.Related(ctx, archived.ID, 6)
		if err != nil || len(books) != 0 {
			t.Fatalf("archived source len=%d err=%v", len(books), err)
		}
	})
	t.Run("facets exclude archived values and count titles", func(t *testing.T) {
		facets, err := repo.Facets(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(facets["languages"]) != 1 || facets["languages"][0].Count != 4 {
			t.Fatalf("languages=%+v", facets["languages"])
		}
		for _, v := range facets["publication_years"] {
			if v.Value == "2026" {
				t.Fatal("archived-only year is public")
			}
		}
		for _, v := range facets["subjects"] {
			if v.Value == "Computing" && v.Count != 3 {
				t.Fatalf("subject count=%d", v.Count)
			}
		}
	})
	t.Run("bookmarks scan rich book metadata", func(t *testing.T) {
		var memberID uuid.UUID
		if err := db.QueryRow(ctx, `INSERT INTO users (identifier,email,full_name,password_hash,category) VALUES ('test','test@example.invalid','Test','unused','undergraduate') RETURNING id`).Scan(&memberID); err != nil {
			t.Fatal(err)
		}
		bookmarks := NewBookmarkRepo(db)
		if _, err := bookmarks.Add(ctx, memberID, source.ID); err != nil {
			t.Fatal(err)
		}
		items, total, err := bookmarks.List(ctx, memberID, 20, 0)
		if err != nil || total != 1 || len(items) != 1 || items[0].Book.Edition != "Second" || items[0].Book.CreatedAt.IsZero() {
			t.Fatalf("bookmarks metadata: %+v total=%d err=%v", items, total, err)
		}
	})
	t.Run("publication year sorts keep unavailable years last", func(t *testing.T) {
		if _, err := db.Exec(ctx, `UPDATE books SET published_year=NULL WHERE id=$1`, source.ID); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(ctx, `UPDATE books SET published_year=2020 WHERE id=$1`, source.ID)
		for _, tc := range []struct {
			sort  string
			first uuid.UUID
		}{{"newest", author.ID}, {"oldest", subject.ID}, {"title", author.ID}} {
			books, total, err := repo.Search(ctx, SearchParams{Sort: tc.sort, Limit: 20})
			if err != nil || total != 4 || len(books) != 4 || books[0].ID != tc.first {
				t.Fatalf("sort %s books=%+v total=%d err=%v", tc.sort, books, total, err)
			}
			if tc.sort != "title" && books[3].ID != source.ID {
				t.Fatalf("sort %s unknown year not last", tc.sort)
			}
		}
	})
	t.Run("new arrivals use cataloguing date and stable total", func(t *testing.T) {
		if _, err := db.Exec(ctx, `UPDATE books SET created_at = '2000-01-01'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE books SET created_at = '2020-01-01' WHERE id=$1`, subject.ID); err != nil {
			t.Fatal(err)
		}
		books, total, err := repo.NewArrivals(ctx, 1, 0)
		if err != nil || total != 4 || len(books) != 1 || books[0].ID != subject.ID {
			t.Fatalf("arrivals total=%d books=%+v err=%v", total, books, err)
		}
		books, total, err = repo.NewArrivals(ctx, 1, 99)
		if err != nil || total != 4 || len(books) != 0 {
			t.Fatalf("arrivals out-of-range total=%d err=%v", total, err)
		}
	})
}
