package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/google/uuid"
)

type editableCatalogue struct {
	fakeCatalogue
	patch     postgres.UpdateBookParams
	calls     int
	updateErr error
}

func (f *editableCatalogue) UpdateBook(_ context.Context, _ uuid.UUID, p postgres.UpdateBookParams) (domain.Book, error) {
	f.calls++
	f.patch = p
	return domain.Book{}, f.updateErr
}
func bookText(v string) *string       { return &v }
func bookYear(v int) *int             { return &v }
func bookNames(v ...string) *[]string { return &v }

func TestBookMetadataPatchValidation(t *testing.T) {
	tests := []struct {
		name string
		p    postgres.UpdateBookParams
	}{
		{"empty", postgres.UpdateBookParams{}},
		{"blank title", postgres.UpdateBookParams{Title: bookText("  ")}},
		{"title limit", postgres.UpdateBookParams{Title: bookText(strings.Repeat("x", 501))}},
		{"dewey", postgres.UpdateBookParams{CallNumber: bookText("005.133")}},
		{"junk lcc suffix", postgres.UpdateBookParams{CallNumber: bookText("QA76<script>")}},
		{"empty lcc", postgres.UpdateBookParams{CallNumber: bookText("")}},
		{"isbn13 checksum", postgres.UpdateBookParams{ISBN13: bookText("9780262033849")}},
		{"isbn13 prefix", postgres.UpdateBookParams{ISBN13: bookText("1234567890128")}},
		{"isbn13 length", postgres.UpdateBookParams{ISBN13: bookText("978026203384")}},
		{"isbn10 checksum", postgres.UpdateBookParams{ISBN10: bookText("0262033845")}},
		{"isbn10 letter", postgres.UpdateBookParams{ISBN10: bookText("0X62033845")}},
		{"ancient year", postgres.UpdateBookParams{PublishedYear: bookYear(999)}},
		{"future year", postgres.UpdateBookParams{PublishedYear: bookYear(time.Now().UTC().Year() + 2)}},
		{"empty author", postgres.UpdateBookParams{Authors: bookNames(" ")}},
		{"null author value", postgres.UpdateBookParams{Authors: bookNames("")}},
		{"duplicate authors", postgres.UpdateBookParams{Authors: bookNames("Author", "Author")}},
		{"duplicate subjects", postgres.UpdateBookParams{Subjects: bookNames("Topic", " Topic ")}},
		{"author limit", postgres.UpdateBookParams{Authors: bookNames(strings.Repeat("a", 201))}},
		{"many subjects", postgres.UpdateBookParams{Subjects: bookNames(make([]string, 101)...)}},
		{"invalid unicode", postgres.UpdateBookParams{Publisher: bookText(string([]byte{0xff}))}},
		{"control", postgres.UpdateBookParams{Language: bookText("Eng\x00lish")}},
		{"description limit", postgres.UpdateBookParams{Description: bookText(strings.Repeat("a", 20001))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &editableCatalogue{}
			svc := service.NewCatalogueService(store)
			test.p.StaffID = uuid.New()
			_, err := svc.UpdateBook(context.Background(), uuid.New(), test.p)
			if !errors.Is(err, domain.ErrInvalidBookMetadata) || store.calls != 0 {
				t.Fatalf("error=%v calls=%d", err, store.calls)
			}
		})
	}
}
func TestBookMetadataPatchNormalisationAndPresence(t *testing.T) {
	store := &editableCatalogue{}
	svc := service.NewCatalogueService(store)
	authorInput := []string{" Cormen ", " Leiserson "}
	subjectInput := []string{"Computing", " Algorithms "}
	p := postgres.UpdateBookParams{StaffID: uuid.New(), Title: bookText(" Algorithms "), ISBN13: bookText("978-0-262-03384-8"), ISBN10: bookText("0-8044-2957-x"), CallNumber: bookText("qa76.6 .i585"), Authors: &authorInput, Subjects: &subjectInput, Publisher: bookText(""), Description: bookText("First paragraph.\nSecond paragraph.")}
	if _, err := svc.UpdateBook(context.Background(), uuid.New(), p); err != nil {
		t.Fatal(err)
	}
	got := store.patch
	if *got.Title != "Algorithms" || *got.ISBN13 != "9780262033848" || *got.ISBN10 != "080442957X" || *got.CallNumber != "qa76.6 .i585" {
		t.Fatalf("normalisation failed: %+v", got)
	}
	if got.Subtitle != nil || got.PublishedYear != nil || got.Publisher == nil || *got.Publisher != "" {
		t.Fatal("omitted and clear fields were confused")
	}
	if (*got.Authors)[0] != "Cormen" || (*got.Subjects)[0] != "Algorithms" || authorInput[0] != " Cormen " {
		t.Fatal("list normalization/order/caller input changed")
	}
	for _, year := range []int{1000, time.Now().UTC().Year() + 1} {
		if _, err := svc.UpdateBook(context.Background(), uuid.New(), postgres.UpdateBookParams{PublishedYear: &year, StaffID: uuid.New()}); err != nil {
			t.Fatal(err)
		}
	}
	empty := []string{}
	if _, err := svc.UpdateBook(context.Background(), uuid.New(), postgres.UpdateBookParams{Subjects: &empty, ISBN13: bookText(""), StaffID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
}
func TestBookMetadataPatchAuthenticationAndErrors(t *testing.T) {
	store := &editableCatalogue{}
	svc := service.NewCatalogueService(store)
	if _, err := svc.UpdateBook(context.Background(), uuid.New(), postgres.UpdateBookParams{Title: bookText("Book")}); !errors.Is(err, domain.ErrUnauthenticated) || store.calls != 0 {
		t.Fatalf("unauthenticated edit: %v", err)
	}
	for _, want := range []error{domain.ErrNotFound, domain.ErrDuplicateISBN} {
		store.updateErr = want
		if _, err := svc.UpdateBook(context.Background(), uuid.New(), postgres.UpdateBookParams{Title: bookText("Book"), StaffID: uuid.New()}); !errors.Is(err, want) {
			t.Fatalf("got%v want%v", err, want)
		}
	}
}
func TestCreateAndPatchShareBookMetadataRules(t *testing.T) {
	for _, p := range []postgres.CreateBookParams{
		{Title: "Book", CallNumber: "QA76", ISBN13: "9780262033849"},
		{Title: "Book", CallNumber: "QA76", PublishedYear: bookYear(time.Now().UTC().Year() + 2)},
		{Title: "Book", CallNumber: "QA76", Authors: []string{""}},
	} {
		svc := service.NewCatalogueService(&fakeCatalogue{})
		if _, err := svc.CreateBook(context.Background(), p); !errors.Is(err, domain.ErrInvalidBookMetadata) {
			t.Fatalf("POST accepted invalid metadata: %v", err)
		}
	}
}
