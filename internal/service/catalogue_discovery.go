package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
)

// CatalogueDiscoveryStore separates read-only discovery from inventory writes.
// Older catalogue adapters continue to satisfy CatalogueStore.
type CatalogueDiscoveryStore interface {
	NewArrivals(context.Context, int, int) ([]domain.Book, int, error)
	Related(context.Context, uuid.UUID, int) ([]domain.Book, error)
	Facets(context.Context) (map[string][]domain.FacetValue, error)
}

func (s *CatalogueService) NewArrivals(ctx context.Context, limit, offset int) ([]domain.Book, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, domain.ErrInvalidSearch
	}
	store, ok := s.books.(CatalogueDiscoveryStore)
	if !ok {
		return nil, 0, domain.ErrNotFound
	}
	return store.NewArrivals(ctx, limit, offset)
}
func (s *CatalogueService) Related(ctx context.Context, id uuid.UUID, limit int) ([]domain.Book, error) {
	if id == uuid.Nil || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidSearch
	}
	store, ok := s.books.(CatalogueDiscoveryStore)
	if !ok {
		return nil, domain.ErrNotFound
	}
	return store.Related(ctx, id, limit)
}
func (s *CatalogueService) Facets(ctx context.Context) (map[string][]domain.FacetValue, error) {
	store, ok := s.books.(CatalogueDiscoveryStore)
	if !ok {
		return nil, domain.ErrNotFound
	}
	return store.Facets(ctx)
}

// MarshalJSON is an explicit public projection. Legacy catalogue names stay
// readable, but adding an internal domain field can never expose it by accident.
func (v BookView) MarshalJSON() ([]byte, error) {
	b := v.Book
	type publicBook struct {
		ID                 uuid.UUID
		Title              string
		Subtitle           string
		ISBN13             string
		ISBN10             string
		Publisher          string
		PlaceOfPublication string
		PublishedYear      *int
		CallNumber         string
		LCCClass           string
		Description        string
		Authors            []string
		Subjects           []string
		Availability       domain.Availability
		Wing               domain.Wing `json:"wing"`
		IsAvailable        bool        `json:"is_available"`
		Borrowable         int         `json:"borrowable"`
		OnShelf            bool        `json:"on_shelf"`
		ShelfCopyRetained  bool        `json:"shelf_copy_retained"`
		Edition            string      `json:"edition,omitempty"`
		PublicationYear    *int        `json:"publication_year,omitempty"`
		PublicPublisher    string      `json:"publisher,omitempty"`
		Language           string      `json:"language,omitempty"`
		PublicDescription  string      `json:"description,omitempty"`
		PublicSubjects     []string    `json:"subjects,omitempty"`
		CreatedAt          *time.Time  `json:"created_at,omitempty"`
	}
	var created *time.Time
	if !b.CreatedAt.IsZero() {
		t := b.CreatedAt.UTC()
		created = &t
	}
	return json.Marshal(publicBook{
		ID: b.ID, Title: b.Title, Subtitle: b.Subtitle, ISBN13: b.ISBN13, ISBN10: b.ISBN10,
		Publisher: b.Publisher, PlaceOfPublication: b.PlaceOfPublication, PublishedYear: b.PublishedYear,
		CallNumber: b.CallNumber, LCCClass: b.LCCClass, Description: b.Description, Authors: b.Authors,
		Subjects: b.Subjects, Availability: b.Availability, Wing: v.Wing, IsAvailable: v.IsAvailable,
		Borrowable: v.Borrowable, OnShelf: v.OnShelf, ShelfCopyRetained: v.ShelfCopyRetained,
		Edition: b.Edition, PublicationYear: b.PublishedYear, PublicPublisher: b.Publisher, Language: b.Language,
		PublicDescription: b.Description, PublicSubjects: b.Subjects, CreatedAt: created,
	})
}
