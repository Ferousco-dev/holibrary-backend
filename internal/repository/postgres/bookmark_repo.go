package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// BookmarkRepo stores the titles a member has saved to come back to.
type BookmarkRepo struct{ db *pgxpool.Pool }

func NewBookmarkRepo(db *pgxpool.Pool) *BookmarkRepo { return &BookmarkRepo{db: db} }

// Add saves a title for a member.
//
// Bookmarking something twice is not a mistake worth reporting: a member who
// taps the button again on a second tab means the same thing they meant the
// first time. ON CONFLICT DO NOTHING makes the repeat a no-op, and the RETURNING
// clause is followed by a plain read so the caller always gets a row back,
// whether this call created it or an earlier one did.
func (r *BookmarkRepo) Add(ctx context.Context, userID, bookID uuid.UUID) (domain.Bookmark, error) {
	var b domain.Bookmark
	err := r.db.QueryRow(ctx, `
		INSERT INTO bookmarks (user_id, book_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, book_id) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING id, user_id, book_id, created_at`,
		userID, bookID).Scan(&b.ID, &b.UserID, &b.BookID, &b.CreatedAt)
	if err != nil {
		// A book_id that matches no title is a foreign key violation, which
		// translate() turns into a not-found rather than a 500.
		return domain.Bookmark{}, translate(err)
	}
	return b, nil
}

// Remove deletes a member's bookmark for a title.
//
// Addressed by book, not by bookmark id, because that is what the interface
// has: a reader looking at a title knows its id and should not have to fetch
// their whole bookmark list to find the row that points at it.
//
// Removing something already absent reports no error. The caller asked for the
// title not to be bookmarked, and it is not.
func (r *BookmarkRepo) Remove(ctx context.Context, userID, bookID uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM bookmarks WHERE user_id = $1 AND book_id = $2`, userID, bookID)
	return translate(err)
}

// Exists reports whether a member has bookmarked a title.
func (r *BookmarkRepo) Exists(ctx context.Context, userID, bookID uuid.UUID) (bool, error) {
	var found bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM bookmarks WHERE user_id = $1 AND book_id = $2)`,
		userID, bookID).Scan(&found)
	return found, translate(err)
}

// List returns a member's bookmarked titles, newest first.
//
// The full book is returned rather than an id, because every screen that shows
// a bookmark shows the title, its authors and whether a copy can be borrowed.
// Returning ids would make the interface fetch each title separately.
//
// created_at DESC then id: created_at alone is not unique, and Postgres may
// order ties differently between requests, which silently repeats and drops
// rows across pages (DEF-008).
func (r *BookmarkRepo) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.BookmarkedBook, int, error) {
	var total int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM bookmarks WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, translate(err)
	}

	rows, err := r.db.Query(ctx, bookFields+`, bm.created_at
		  FROM bookmarks bm
		  JOIN books b ON b.id = bm.book_id
		 WHERE bm.user_id = $1
		 ORDER BY bm.created_at DESC, bm.id
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	out := make([]domain.BookmarkedBook, 0, limit)
	for rows.Next() {
		var item domain.BookmarkedBook
		b := &item.Book
		if err := rows.Scan(&b.ID, &b.Title, &b.Subtitle, &b.ISBN13, &b.ISBN10,
			&b.Publisher, &b.PlaceOfPublication, &b.PublishedYear, &b.CallNumber,
			&b.LCCClass, &b.Description, &b.Status, &b.Authors, &b.Subjects,
			&b.Availability.TotalCopies, &b.Availability.Available,
			&b.Availability.OnLoan, &b.Availability.NotForLoan, &b.Availability.Stock,
			&item.SavedAt); err != nil {
			return nil, 0, translate(err)
		}
		out = append(out, item)
	}
	return out, total, translate(rows.Err())
}
