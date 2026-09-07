package service

import (
	"context"
	"encoding/json"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SavedSearchLimit = 20

type SavedSearchStore interface {
	Create(context.Context, uuid.UUID, string, map[string]json.RawMessage, int) (domain.SavedSearch, error)
	ListForUser(context.Context, uuid.UUID) ([]domain.SavedSearch, error)
	Delete(context.Context, uuid.UUID, uuid.UUID) error
}

type SavedSearchService struct{ searches SavedSearchStore }

func NewSavedSearchService(s SavedSearchStore) *SavedSearchService {
	return &SavedSearchService{searches: s}
}
func (s *SavedSearchService) Create(ctx context.Context, memberID uuid.UUID, name string, query map[string]json.RawMessage) (domain.SavedSearch, error) {
	if memberID == uuid.Nil {
		return domain.SavedSearch{}, domain.ErrUnauthenticated
	}
	if !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return domain.SavedSearch{}, domain.ErrInvalidSearch
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 || query == nil {
		return domain.SavedSearch{}, domain.ErrInvalidSearch
	}
	if err := ValidateSavedQuery(query); err != nil {
		return domain.SavedSearch{}, err
	}
	return s.searches.Create(ctx, memberID, name, query, SavedSearchLimit)
}
func (s *SavedSearchService) List(ctx context.Context, memberID uuid.UUID) ([]domain.SavedSearch, error) {
	if memberID == uuid.Nil {
		return nil, domain.ErrUnauthenticated
	}
	return s.searches.ListForUser(ctx, memberID)
}
func (s *SavedSearchService) Delete(ctx context.Context, id, memberID uuid.UUID) error {
	if memberID == uuid.Nil {
		return domain.ErrUnauthenticated
	}
	if id == uuid.Nil {
		return domain.ErrNotFound
	}
	return s.searches.Delete(ctx, id, memberID)
}
