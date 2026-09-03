package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

type CatalogueRepo struct{ db *pgxpool.Pool }

func NewCatalogueRepo(db *pgxpool.Pool) *CatalogueRepo { return &CatalogueRepo{db: db} }

// SearchParams describes a catalogue query.
//
// The separate title, author and subject fields are not decoration: they are the
// three access points the card catalogue has always provided, and HOL's own OPAC
// searches "author, title and subject entries, keywords in titles" (DOM-007).
type SearchParams struct {
	Query         string // free-text across all three access points
	Title         string
	Author        string
	Subject       string
	ISBN          string
	CallNumber    string
	LCCClass      string
	OnlyAvailable bool
	Limit         int
	Offset        int
}

// availabilitySelect counts copy states inline.
//
// Availability is computed here, on every read, rather than kept in a column.
// A stored counter is a second source of truth and drifts the first time an
// update fails halfway (REQ-038, REQ-039).
const availabilitySelect = `
	(SELECT count(*) FROM copies c
	  WHERE c.book_id = b.id AND c.status <> 'withdrawn')                    AS total_copies,
	(SELECT count(*) FROM copies c
	  WHERE c.book_id = b.id AND c.status = 'available'
	    AND c.loan_policy = 'circulating')                                   AS available,
	(SELECT count(*) FROM copies c
	  WHERE c.book_id = b.id AND c.status = 'on_loan')                       AS on_loan,
	(SELECT count(*) FROM copies c
	  WHERE c.book_id = b.id AND c.status = 'available'
	    AND c.loan_policy <> 'circulating')                                  AS not_for_loan`

// bookFields and bookFrom are kept apart so the window-function count used for
// pagination can be appended to the select list, before FROM. Concatenating it
// onto a string that already ended in FROM produced a syntax error: DEF-002.
const bookFields = `
	SELECT b.id, b.title, coalesce(b.subtitle,''), coalesce(b.isbn13,''),
	       coalesce(b.isbn10,''), coalesce(b.publisher,''),
	       coalesce(b.place_of_publication,''), b.published_year,
	       b.call_number, b.lcc_class, coalesce(b.description,''), b.status,
	       coalesce((SELECT array_agg(a.name ORDER BY ba.position)
	                   FROM book_authors ba JOIN authors a ON a.id = ba.author_id
	                  WHERE ba.book_id = b.id), '{}')                        AS authors,
	       coalesce((SELECT array_agg(s.heading ORDER BY s.heading)
	                   FROM book_subjects bs JOIN subjects s ON s.id = bs.subject_id
	                  WHERE bs.book_id = b.id), '{}')                        AS subjects,` +
	availabilitySelect

const bookFrom = `
	  FROM books b`

const bookSelect = bookFields + bookFrom

func scanBook(row pgx.Row) (domain.Book, error) {
	var b domain.Book
	err := row.Scan(&b.ID, &b.Title, &b.Subtitle, &b.ISBN13, &b.ISBN10,
		&b.Publisher, &b.PlaceOfPublication, &b.PublishedYear, &b.CallNumber,
		&b.LCCClass, &b.Description, &b.Status, &b.Authors, &b.Subjects,
		&b.Availability.TotalCopies, &b.Availability.Available,
		&b.Availability.OnLoan, &b.Availability.NotForLoan)
	return b, err
}

// Search runs a catalogue query.
//
// Every filter is a bound parameter and the WHERE clause is fixed text: an empty
// parameter disables its own condition. Building the clause by concatenation
// would be the classic injection vector, so it is not done (NFR-008).
func (r *CatalogueRepo) Search(ctx context.Context, p SearchParams) ([]domain.Book, int, error) {
	q := bookFields + `,
	       count(*) OVER() AS total` + bookFrom + `
	 WHERE b.status = 'active'
	   AND ($1 = '' OR b.search_vector @@ plainto_tsquery('english', $1))
	   AND ($2 = '' OR b.title ILIKE '%' || $2 || '%')
	   AND ($3 = '' OR EXISTS (SELECT 1 FROM book_authors ba
	                             JOIN authors a ON a.id = ba.author_id
	                            WHERE ba.book_id = b.id AND a.name ILIKE '%' || $3 || '%'))
	   AND ($4 = '' OR EXISTS (SELECT 1 FROM book_subjects bs
	                             JOIN subjects s ON s.id = bs.subject_id
	                            WHERE bs.book_id = b.id AND s.heading ILIKE '%' || $4 || '%'))
	   AND ($5 = '' OR b.isbn13 = $5 OR b.isbn10 = $5)
	   AND ($6 = '' OR b.call_number ILIKE $6 || '%')
	   AND ($7 = '' OR b.lcc_class = $7)
	   AND (NOT $8 OR EXISTS (SELECT 1 FROM copies c
	                           WHERE c.book_id = b.id AND c.status = 'available'
	                             AND c.loan_policy = 'circulating'))
	 ORDER BY
	   CASE WHEN $1 = '' THEN 0
	        ELSE ts_rank(b.search_vector, plainto_tsquery('english', $1)) END DESC,
	   b.title
	 LIMIT $9 OFFSET $10`

	rows, err := r.db.Query(ctx, q, p.Query, p.Title, p.Author, p.Subject,
		strings.ReplaceAll(p.ISBN, "-", ""), p.CallNumber, p.LCCClass,
		p.OnlyAvailable, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	var books []domain.Book
	total := 0
	for rows.Next() {
		var b domain.Book
		if err := rows.Scan(&b.ID, &b.Title, &b.Subtitle, &b.ISBN13, &b.ISBN10,
			&b.Publisher, &b.PlaceOfPublication, &b.PublishedYear, &b.CallNumber,
			&b.LCCClass, &b.Description, &b.Status, &b.Authors, &b.Subjects,
			&b.Availability.TotalCopies, &b.Availability.Available,
			&b.Availability.OnLoan, &b.Availability.NotForLoan, &total); err != nil {
			return nil, 0, translate(err)
		}
		books = append(books, b)
	}
	return books, total, rows.Err()
}

func (r *CatalogueRepo) FindBook(ctx context.Context, id uuid.UUID) (domain.Book, error) {
	b, err := scanBook(r.db.QueryRow(ctx, bookSelect+` WHERE b.id = $1`, id))
	return b, translate(err)
}

// CreateBookParams carries a new bibliographic record.
type CreateBookParams struct {
	Title              string
	Subtitle           string
	ISBN13             string
	ISBN10             string
	Publisher          string
	PlaceOfPublication string
	PublishedYear      *int
	CallNumber         string
	Description        string
	Authors            []string
	Subjects           []string
}

// CreateBook inserts a book with its authors and subjects in one transaction,
// so a title can never end up in the catalogue without its access points.
func (r *CatalogueRepo) CreateBook(ctx context.Context, p CreateBookParams) (domain.Book, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Book{}, translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	// The class mark's leading letter is stored so the shelf wing can be derived
	// and the catalogue browsed by LCC class (DOM-003).
	lccClass := strings.ToUpper(strings.TrimSpace(p.CallNumber))
	if lccClass == "" {
		return domain.Book{}, domain.ErrInvalidCallNumber
	}
	lccClass = lccClass[:1]

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO books (title, subtitle, isbn13, isbn10, publisher,
		                   place_of_publication, published_year, call_number,
		                   lcc_class, description)
		VALUES ($1,$2,nullif($3,''),nullif($4,''),nullif($5,''),nullif($6,''),
		        $7,$8,$9,nullif($10,''))
		RETURNING id`,
		p.Title, nullif(p.Subtitle), p.ISBN13, p.ISBN10, p.Publisher,
		p.PlaceOfPublication, p.PublishedYear, strings.TrimSpace(p.CallNumber),
		lccClass, p.Description).Scan(&id)
	if err != nil {
		return domain.Book{}, translate(err)
	}

	if err := linkAuthors(ctx, tx, id, p.Authors); err != nil {
		return domain.Book{}, err
	}
	if err := linkSubjects(ctx, tx, id, p.Subjects); err != nil {
		return domain.Book{}, err
	}

	// Refresh the weighted search vector now that authors and subjects exist.
	if _, err := tx.Exec(ctx, `SELECT books_refresh_search_vector($1)`, id); err != nil {
		return domain.Book{}, translate(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Book{}, translate(err)
	}
	return r.FindBook(ctx, id)
}

// linkAuthors upserts author names and attaches them in entry order. Position 1
// is the main entry, mirroring the author card of the physical catalogue.
func linkAuthors(ctx context.Context, tx pgx.Tx, bookID uuid.UUID, names []string) error {
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var authorID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO authors (name) VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, name).Scan(&authorID); err != nil {
			return translate(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO book_authors (book_id, author_id, position)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, bookID, authorID, i+1); err != nil {
			return translate(err)
		}
	}
	return nil
}

func linkSubjects(ctx context.Context, tx pgx.Tx, bookID uuid.UUID, headings []string) error {
	for _, heading := range headings {
		heading = strings.TrimSpace(heading)
		if heading == "" {
			continue
		}
		var subjectID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO subjects (heading) VALUES ($1)
			ON CONFLICT (heading) DO UPDATE SET heading = EXCLUDED.heading
			RETURNING id`, heading).Scan(&subjectID); err != nil {
			return translate(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO book_subjects (book_id, subject_id)
			VALUES ($1,$2) ON CONFLICT DO NOTHING`, bookID, subjectID); err != nil {
			return translate(err)
		}
	}
	return nil
}

// ArchiveBook hides a title from search without deleting it, because its loan
// history must survive (DOM-008, REQ-020).
func (r *CatalogueRepo) ArchiveBook(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE books SET status = 'archived', updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// AddCopy registers one physical volume against a title.
//
// The accession number is the library's own per-item identifier and is unique
// across the whole collection; the call number it inherits from the book is not
// (DOM-002, REQ-022, REQ-023).
func (r *CatalogueRepo) AddCopy(ctx context.Context, bookID uuid.UUID,
	accession string, policy domain.LoanPolicy) (domain.Copy, error) {

	const q = `INSERT INTO copies (book_id, accession_number, loan_policy)
	           VALUES ($1,$2,$3)
	           RETURNING id, book_id, accession_number, loan_policy, status,
	                     acquired_at, coalesce(notes,'')`

	var c domain.Copy
	err := r.db.QueryRow(ctx, q, bookID, strings.TrimSpace(accession), policy).Scan(
		&c.ID, &c.BookID, &c.AccessionNumber, &c.LoanPolicy, &c.Status,
		&c.AcquiredAt, &c.Notes)
	if err != nil {
		if isUniqueViolation(err, "copies_accession_number_key") {
			return domain.Copy{}, domain.ErrDuplicateAccession
		}
		return domain.Copy{}, translate(err)
	}
	return c, nil
}

// ListCopies returns every volume of a title, for the librarian's inventory view.
func (r *CatalogueRepo) ListCopies(ctx context.Context, bookID uuid.UUID) ([]domain.Copy, error) {
	const q = `SELECT id, book_id, accession_number, loan_policy, status,
	                  acquired_at, coalesce(notes,'')
	             FROM copies WHERE book_id = $1 ORDER BY accession_number`

	rows, err := r.db.Query(ctx, q, bookID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var copies []domain.Copy
	for rows.Next() {
		var c domain.Copy
		if err := rows.Scan(&c.ID, &c.BookID, &c.AccessionNumber, &c.LoanPolicy,
			&c.Status, &c.AcquiredAt, &c.Notes); err != nil {
			return nil, translate(err)
		}
		copies = append(copies, c)
	}
	return copies, rows.Err()
}

// UpdateCopy changes a volume's policy or status, which is how a librarian marks
// an item lost, damaged or withdrawn without erasing its history (REQ-024..026).
func (r *CatalogueRepo) UpdateCopy(ctx context.Context, id uuid.UUID,
	policy *domain.LoanPolicy, status *domain.CopyStatus) error {

	const q = `UPDATE copies
	              SET loan_policy = coalesce($2, loan_policy),
	                  status      = coalesce($3, status),
	                  updated_at  = now()
	            WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, id, policy, status)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func nullif(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
