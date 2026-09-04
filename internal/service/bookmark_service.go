package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// Bookmarks is the persistence this service needs.
type Bookmarks interface {
	Add(ctx context.Context, userID, bookID uuid.UUID) (domain.Bookmark, error)
	Remove(ctx context.Context, userID, bookID uuid.UUID) error
	Exists(ctx context.Context, userID, bookID uuid.UUID) (bool, error)
	List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.BookmarkedBook, int, error)
}

// BookmarkService lets a member keep a list of titles to come back to.
//
// There is no librarian view of this and no endpoint that reads another
// member's bookmarks. What somebody is thinking of reading is their business:
// a reading list is exactly the kind of record that should not be casually
// browsable by staff, and the cheapest way to guarantee that is to build no
// way to ask.
type BookmarkService struct{ bookmarks Bookmarks }

func NewBookmarkService(b Bookmarks) *BookmarkService {
	return &BookmarkService{bookmarks: b}
}

// Save adds a title to the member's list.
//
// Saving the same title twice succeeds and changes nothing. A member who taps
// the button again on a second tab means what they meant the first time, and
// an error there would be a report of a problem that does not exist.
func (s *BookmarkService) Save(ctx context.Context, userID, bookID uuid.UUID) (domain.Bookmark, error) {
	return s.bookmarks.Add(ctx, userID, bookID)
}

// Remove takes a title off the member's list. Removing one that is not there
// is not an error: the caller asked for the title not to be saved, and it is not.
func (s *BookmarkService) Remove(ctx context.Context, userID, bookID uuid.UUID) error {
	return s.bookmarks.Remove(ctx, userID, bookID)
}

// IsSaved reports whether this member has saved this title, so a title page can
// draw the control in the right state.
func (s *BookmarkService) IsSaved(ctx context.Context, userID, bookID uuid.UUID) (bool, error) {
	return s.bookmarks.Exists(ctx, userID, bookID)
}

// List returns the member's saved titles, newest first.
//
// The member is always the caller. This service takes a user id from the token
// and never from a request body, so there is no path by which one member's list
// can be read as another's.
func (s *BookmarkService) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.BookmarkedBook, int, error) {
	return s.bookmarks.List(ctx, userID, limit, offset)
}
