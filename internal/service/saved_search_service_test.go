package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
	"strings"
	"testing"
)

type savedStoreStub struct {
	called bool
	member uuid.UUID
	name   string
	limit  int
}

func (s *savedStoreStub) Create(_ context.Context, id uuid.UUID, name string, q map[string]json.RawMessage, limit int) (domain.SavedSearch, error) {
	s.called = true
	s.member = id
	s.name = name
	s.limit = limit
	return domain.SavedSearch{UserID: id, Name: name, Query: q}, nil
}
func (s *savedStoreStub) ListForUser(_ context.Context, id uuid.UUID) ([]domain.SavedSearch, error) {
	s.called = true
	s.member = id
	return nil, nil
}
func (s *savedStoreStub) Delete(_ context.Context, id, user uuid.UUID) error {
	s.called = true
	s.member = user
	return nil
}
func TestSavedSearchValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query map[string]json.RawMessage
	}{
		{"Bad\nname", map[string]json.RawMessage{}}, {string([]byte{0xff}), map[string]json.RawMessage{}}, {"", map[string]json.RawMessage{}}, {strings.Repeat("x", 101), map[string]json.RawMessage{}}, {"valid", nil}, {"valid", map[string]json.RawMessage{"member_id": json.RawMessage(`"x"`)}}, {"valid", map[string]json.RawMessage{"available": json.RawMessage(`"true"`)}},
	} {
		store := &savedStoreStub{}
		_, err := NewSavedSearchService(store).Create(context.Background(), uuid.New(), tc.name, tc.query)
		if err == nil || store.called {
			t.Fatalf("accepted invalid name/query: %q %v", tc.name, tc.query)
		}
	}
	store := &savedStoreStub{}
	id := uuid.New()
	_, err := NewSavedSearchService(store).Create(context.Background(), id, "  My books  ", map[string]json.RawMessage{"available": json.RawMessage(`true`)})
	if err != nil || store.member != id || store.name != "My books" || store.limit != 20 {
		t.Fatalf("create = %+v, %v", store, err)
	}
}
func TestSavedSearchRequiresIdentity(t *testing.T) {
	s := NewSavedSearchService(&savedStoreStub{})
	ctx := context.Background()
	_, err := s.Create(ctx, uuid.Nil, "Books", map[string]json.RawMessage{})
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal(err)
	}
	_, err = s.List(ctx, uuid.Nil)
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, uuid.New(), uuid.Nil); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal(err)
	}
}
