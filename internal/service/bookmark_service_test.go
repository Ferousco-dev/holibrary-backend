package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

// fakeBookmarks records what it was asked for, so a test can assert on the
// arguments the service passed down rather than only on what came back.
type fakeBookmarks struct {
	rows      map[string]bool // "user:book"
	listedFor uuid.UUID
	addCalls  int
}

func newFakeBookmarks() *fakeBookmarks {
	return &fakeBookmarks{rows: map[string]bool{}}
}

func key(u, b uuid.UUID) string { return u.String() + ":" + b.String() }

func (f *fakeBookmarks) Add(_ context.Context, u, b uuid.UUID) (domain.Bookmark, error) {
	f.addCalls++
	f.rows[key(u, b)] = true
	return domain.Bookmark{UserID: u, BookID: b}, nil
}
func (f *fakeBookmarks) Remove(_ context.Context, u, b uuid.UUID) error {
	delete(f.rows, key(u, b))
	return nil
}
func (f *fakeBookmarks) Exists(_ context.Context, u, b uuid.UUID) (bool, error) {
	return f.rows[key(u, b)], nil
}
func (f *fakeBookmarks) List(_ context.Context, u uuid.UUID, _, _ int) ([]domain.BookmarkedBook, int, error) {
	f.listedFor = u
	out := []domain.BookmarkedBook{}
	for k := range f.rows {
		if k[:36] == u.String() {
			out = append(out, domain.BookmarkedBook{})
		}
	}
	return out, len(out), nil
}

// Saving a title twice must succeed and leave one bookmark. A member who taps
// the button again on a second tab means what they meant the first time.
func TestSavingTwiceIsNotAnError(t *testing.T) {
	repo := newFakeBookmarks()
	svc := service.NewBookmarkService(repo)
	user, book := uuid.New(), uuid.New()

	for i := 0; i < 2; i++ {
		if _, err := svc.Save(context.Background(), user, book); err != nil {
			t.Fatalf("save %d returned an error: %v", i+1, err)
		}
	}

	saved, _ := svc.IsSaved(context.Background(), user, book)
	if !saved {
		t.Fatal("the title is not saved after two saves")
	}
	if len(repo.rows) != 1 {
		t.Fatalf("%d bookmark rows, want 1", len(repo.rows))
	}
}

// Removing a bookmark that was never there is not an error. The caller asked
// for the title not to be saved, and it is not.
func TestRemovingSomethingNotSavedIsNotAnError(t *testing.T) {
	svc := service.NewBookmarkService(newFakeBookmarks())
	if err := svc.Remove(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("removing an absent bookmark returned: %v", err)
	}
}

// The service must read the list for the member it was given and nobody else.
//
// This is the privacy property the feature rests on: a reading list says what
// somebody is thinking about, and there is deliberately no endpoint that reads
// another member's. The test asserts the user id travels through untouched, so
// a later refactor cannot quietly widen it.
func TestListReadsOnlyTheGivenMembersBookmarks(t *testing.T) {
	repo := newFakeBookmarks()
	svc := service.NewBookmarkService(repo)

	ada, tunde := uuid.New(), uuid.New()
	book := uuid.New()
	_, _ = svc.Save(context.Background(), ada, book)
	_, _ = svc.Save(context.Background(), tunde, uuid.New())
	_, _ = svc.Save(context.Background(), tunde, uuid.New())

	list, total, err := svc.List(context.Background(), ada, 20, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if repo.listedFor != ada {
		t.Fatalf("asked the repository for %s, want %s", repo.listedFor, ada)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("got %d of Ada's bookmarks (total %d), want 1; Tunde's are leaking", len(list), total)
	}
}

// One member's bookmark must not appear as another's.
func TestOneMembersBookmarkIsNotAnothers(t *testing.T) {
	repo := newFakeBookmarks()
	svc := service.NewBookmarkService(repo)

	ada, tunde, book := uuid.New(), uuid.New(), uuid.New()
	_, _ = svc.Save(context.Background(), ada, book)

	saved, err := svc.IsSaved(context.Background(), tunde, book)
	if err != nil {
		t.Fatalf("IsSaved: %v", err)
	}
	if saved {
		t.Fatal("Tunde is told he saved a title only Ada saved")
	}
}
