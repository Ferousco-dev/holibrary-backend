package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

type fakeCatalogue struct {
	created  postgres.CreateBookParams
	searched postgres.SearchParams
	book     domain.Book
}

func (f *fakeCatalogue) Search(_ context.Context, p postgres.SearchParams) ([]domain.Book, int, error) {
	f.searched = p
	return nil, 0, nil
}
func (f *fakeCatalogue) FindBook(context.Context, uuid.UUID) (domain.Book, error) {
	return f.book, nil
}
func (f *fakeCatalogue) CreateBook(_ context.Context, p postgres.CreateBookParams) (domain.Book, error) {
	f.created = p
	return domain.Book{Title: p.Title, CallNumber: p.CallNumber}, nil
}
func (f *fakeCatalogue) ArchiveBook(context.Context, uuid.UUID) error { return nil }
func (f *fakeCatalogue) AddCopy(_ context.Context, _ uuid.UUID, a string, p domain.LoanPolicy) (domain.Copy, error) {
	return domain.Copy{AccessionNumber: a, LoanPolicy: p}, nil
}
func (f *fakeCatalogue) ListCopies(context.Context, uuid.UUID) ([]domain.Copy, error) {
	return nil, nil
}
func (f *fakeCatalogue) UpdateCopy(context.Context, uuid.UUID, *domain.LoanPolicy, *domain.CopyStatus) error {
	return nil
}

// HOL classifies with Library of Congress, not Dewey, so a bare Dewey-style
// number is a data-entry mistake rather than a valid call number (DOM-001).
func TestCreateBookValidatesLCCCallNumber(t *testing.T) {
	cases := []struct {
		callNumber string
		valid      bool
	}{
		{"QA76.73", true},
		{"DT 515.15 .Ob21", true}, // the worked example from the LIB 001 material
		{"Z1003", true},
		{"005.133", false}, // Dewey, not LCC
		{"", false},
		{"not a call number", false},
	}
	for _, c := range cases {
		t.Run(c.callNumber, func(t *testing.T) {
			svc := service.NewCatalogueService(&fakeCatalogue{})
			_, err := svc.CreateBook(context.Background(), postgres.CreateBookParams{
				Title: "Test Title", CallNumber: c.callNumber,
			})
			if c.valid && err != nil {
				t.Errorf("call number %q should be accepted, got %v", c.callNumber, err)
			}
			if !c.valid && !errors.Is(err, domain.ErrInvalidCallNumber) {
				t.Errorf("call number %q should be rejected, got %v", c.callNumber, err)
			}
		})
	}
}

// A reader who types the ISBN off the back cover, with hyphens, must find the
// same book as one who types it without.
func TestCreateBookNormalisesISBN(t *testing.T) {
	store := &fakeCatalogue{}
	svc := service.NewCatalogueService(store)

	if _, err := svc.CreateBook(context.Background(), postgres.CreateBookParams{
		Title: "Clean Code", CallNumber: "QA76.76", ISBN13: "978-0-13-235088-4",
	}); err != nil {
		t.Fatal(err)
	}
	if store.created.ISBN13 != "9780132350884" {
		t.Errorf("ISBN13 = %q, want 9780132350884", store.created.ISBN13)
	}
}

// An unbounded page size would let one request pull the whole collection.
func TestSearchClampsPageSize(t *testing.T) {
	store := &fakeCatalogue{}
	svc := service.NewCatalogueService(store)

	for _, requested := range []int{0, -5, 5000} {
		if _, _, err := svc.Search(context.Background(), postgres.SearchParams{Limit: requested}); err != nil {
			t.Fatal(err)
		}
		if store.searched.Limit != 20 {
			t.Errorf("limit %d was not clamped, got %d", requested, store.searched.Limit)
		}
	}

	if _, _, err := svc.Search(context.Background(), postgres.SearchParams{Limit: 50}); err != nil {
		t.Fatal(err)
	}
	if store.searched.Limit != 50 {
		t.Errorf("a reasonable limit should be preserved, got %d", store.searched.Limit)
	}
}

// The book view carries the derived facts a reader needs: is a copy free, and
// which wing shelves it (DOM-003).
func TestBookViewDerivesWingAndAvailability(t *testing.T) {
	south := service.NewBookView(domain.Book{
		LCCClass:     "D",
		Availability: domain.Availability{TotalCopies: 5, Available: 3, OnLoan: 2},
	})
	if south.Wing != domain.WingSouth {
		t.Errorf("class D is shelved in the South wing, got %q", south.Wing)
	}
	if !south.IsAvailable {
		t.Error("three free copies means the title is available")
	}

	north := service.NewBookView(domain.Book{
		LCCClass:     "Q",
		Availability: domain.Availability{TotalCopies: 2, Available: 0, OnLoan: 2},
	})
	if north.Wing != domain.WingNorth {
		t.Errorf("class Q is shelved in the North wing, got %q", north.Wing)
	}
	if north.IsAvailable {
		t.Error("every copy on loan means the title is not available")
	}
}
