package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

// CatalogueStore is the persistence the catalogue needs.
type CatalogueStore interface {
	Search(ctx context.Context, p postgres.SearchParams) ([]domain.Book, int, error)
	FindBook(ctx context.Context, id uuid.UUID) (domain.Book, error)
	FindBookByISBN(ctx context.Context, isbn string) (domain.Book, error)
	CreateBook(ctx context.Context, p postgres.CreateBookParams) (domain.Book, error)
	ArchiveBook(ctx context.Context, id, staffID uuid.UUID) error
	AddCopy(ctx context.Context, bookID uuid.UUID, accession string, policy domain.LoanPolicy, staffID uuid.UUID) (domain.Copy, error)
	ListCopies(ctx context.Context, bookID uuid.UUID) ([]domain.Copy, error)
	FindCopy(ctx context.Context, id uuid.UUID) (domain.Copy, error)
	UpdateCopy(ctx context.Context, id uuid.UUID, policy *domain.LoanPolicy, status *domain.CopyStatus, staffID uuid.UUID) error
	SetCopyStatusClosingLoan(ctx context.Context, id uuid.UUID, status domain.CopyStatus, staffID uuid.UUID) error
}

type CatalogueService struct{ books CatalogueStore }

func NewCatalogueService(b CatalogueStore) *CatalogueService { return &CatalogueService{books: b} }

// callNumberPattern matches a Library of Congress class mark: one or three
// letters, then digits, optionally a decimal and a Cutter number.
// Examples: "QA76.73", "DT 515.15 .Ob21", "Z1003".
//
// HOL classifies with LCC rather than Dewey, so a purely numeric Dewey-style
// number is rejected as a data-entry mistake (DOM-001).
var callNumberPattern = regexp.MustCompile(`^[A-Z]{1,3}\s?\d`)

// Search runs a catalogue query across the access points the card catalogue has
// always offered -- author, title and subject (DOM-007, REQ-028..035).
func (s *CatalogueService) Search(ctx context.Context, p postgres.SearchParams) ([]domain.Book, int, error) {
	// Bound the page size so a single request cannot ask for the whole
	// collection and exhaust the free-tier database's memory.
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 20
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	p.LCCClass = strings.ToUpper(strings.TrimSpace(p.LCCClass))
	return s.books.Search(ctx, p)
}

// BookView is a book with the derived facts a reader actually needs: whether a
// copy is free, and which way to walk once inside the building.
type BookView struct {
	domain.Book
	Wing domain.Wing `json:"wing"`
	// IsAvailable means a copy may actually be taken away, not merely that one
	// sits on the shelf.
	IsAvailable bool `json:"is_available"`
	// Borrowable is the shelf count minus any copy held back by the last-copy
	// retention policy (DEC-018).
	Borrowable int `json:"borrowable"`
	// OnShelf tells a reader they can come and consult the title even when
	// nothing may be borrowed.
	OnShelf bool `json:"on_shelf"`
	// ShelfCopyRetained explains why a title on the shelf cannot be borrowed.
	ShelfCopyRetained bool `json:"shelf_copy_retained"`
}

func NewBookView(b domain.Book) BookView {
	a := b.Availability
	return BookView{
		Book: b, Wing: b.Wing(),
		IsAvailable:       a.IsAvailable(),
		Borrowable:        a.Borrowable(),
		OnShelf:           a.OnShelf(),
		ShelfCopyRetained: domain.RetainsAShelfCopy(a.Stock) && a.Available == 1,
	}
}

func (s *CatalogueService) Get(ctx context.Context, id uuid.UUID) (BookView, []domain.Copy, error) {
	book, err := s.books.FindBook(ctx, id)
	if err != nil {
		return BookView{}, nil, err
	}
	copies, err := s.books.ListCopies(ctx, id)
	if err != nil {
		return BookView{}, nil, err
	}
	return NewBookView(book), copies, nil
}

// CreateBook adds a bibliographic record (REQ-016).
func (s *CatalogueService) CreateBook(ctx context.Context, p postgres.CreateBookParams) (domain.Book, error) {
	if strings.TrimSpace(p.Title) == "" {
		return domain.Book{}, domain.ErrInvalidCallNumber
	}
	if !callNumberPattern.MatchString(strings.ToUpper(strings.TrimSpace(p.CallNumber))) {
		return domain.Book{}, domain.ErrInvalidCallNumber
	}
	p.ISBN13 = normaliseISBN(p.ISBN13)
	p.ISBN10 = normaliseISBN(p.ISBN10)
	return s.books.CreateBook(ctx, p)
}

// FindByISBN returns a title the library already holds, if it does.
func (s *CatalogueService) FindByISBN(ctx context.Context, isbn string) (domain.Book, error) {
	return s.books.FindBookByISBN(ctx, normaliseISBN(isbn))
}

// normaliseISBN strips the punctuation people type so that "978-0-13-235088-4"
// and "9780132350884" are the same book to a search.
func normaliseISBN(isbn string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(isbn))
}

// Archive hides a title from the catalogue without deleting it, because its
// loan history has to survive (DOM-008, REQ-020).
func (s *CatalogueService) Archive(ctx context.Context, id, staffID uuid.UUID) error {
	return s.books.ArchiveBook(ctx, id, staffID)
}

// AddCopy registers one physical volume against a title (REQ-022).
func (s *CatalogueService) AddCopy(ctx context.Context, bookID uuid.UUID, accession string, policy domain.LoanPolicy, staffID uuid.UUID) (domain.Copy, error) {
	if strings.TrimSpace(accession) == "" {
		return domain.Copy{}, domain.ErrDuplicateAccession
	}
	if policy == "" {
		policy = domain.PolicyCirculating
	}
	return s.books.AddCopy(ctx, bookID, accession, policy, staffID)
}

// UpdateCopy changes a volume's loan policy or status, enforcing the copy state
// machine (DEF-009).
//
// The dangerous case this guards is a librarian setting a borrowed copy back to
// available: the loan would stay open, and the library would believe a book was
// on the shelf while a student still held it. Marking a borrowed copy lost or
// damaged is permitted and closes the loan honestly, so nobody has to record a
// fake return in order to write down a real loss.
func (s *CatalogueService) UpdateCopy(ctx context.Context, id uuid.UUID,
	policy *domain.LoanPolicy, status *domain.CopyStatus, staffID uuid.UUID) error {

	current, err := s.books.FindCopy(ctx, id)
	if err != nil {
		return err
	}

	if status != nil && *status != current.Status {
		if !current.Status.CanTransitionTo(*status) {
			return domain.ErrInvalidTransition
		}
		if current.Status == domain.CopyOnLoan && status.ClosesAnOpenLoan() {
			// Closing the loan and moving the copy must happen together.
			if err := s.books.SetCopyStatusClosingLoan(ctx, id, *status, staffID); err != nil {
				return err
			}
			status = nil // already applied
		}
	}

	// A copy that is out cannot have its lending policy rewritten underneath an
	// active loan.
	if policy != nil && current.Status == domain.CopyOnLoan {
		return domain.ErrCopyOnLoan
	}

	if policy == nil && status == nil {
		return nil
	}
	return s.books.UpdateCopy(ctx, id, policy, status, staffID)
}
