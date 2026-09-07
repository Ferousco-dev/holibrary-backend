package postgres

import (
	"context"
	"strings"

	"time"

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
	Faculty       string
	Department    string
	Language      string
	Wing          string
	YearFrom      *int
	YearTo        *int
	Available     *bool
	Borrowable    *bool
	Sort          string
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
	    AND c.loan_policy <> 'circulating')                                  AS not_for_loan,
	-- The lending stock: circulating copies on the shelf or out. Lost, damaged
	-- and withdrawn volumes are not stock, so losing a copy correctly relaxes
	-- the last-copy retention rule rather than tightening it (DEC-018).
	(SELECT count(*) FROM copies c
	  WHERE c.book_id = b.id AND c.loan_policy = 'circulating'
	    AND c.status IN ('available','on_loan'))                             AS stock`

// bookFields and bookFrom are kept apart so bookmark metadata can be appended
// to the select list before FROM (DEF-002).
const bookFields = `
	SELECT b.id, b.title, coalesce(b.subtitle,''), coalesce(b.isbn13,''),
	       coalesce(b.isbn10,''), coalesce(b.publisher,''),
	       coalesce(b.place_of_publication,''), b.published_year,
	       b.call_number, b.lcc_class, coalesce(b.description,''), b.status,
 coalesce(b.edition,''), coalesce(b.language,''), coalesce(b.faculty,''), coalesce(b.department,''), b.created_at,
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
		&b.LCCClass, &b.Description, &b.Status, &b.Edition, &b.Language, &b.Faculty, &b.Department, &b.CreatedAt, &b.Authors, &b.Subjects,
		&b.Availability.TotalCopies, &b.Availability.Available,
		&b.Availability.OnLoan, &b.Availability.NotForLoan, &b.Availability.Stock)
	return b, err
}

// wingExpression mirrors domain.WingFor using the stored LCC class.
const wingExpression = `CASE WHEN b.lcc_class BETWEEN 'A' AND 'J' THEN 'South'
 WHEN b.lcc_class BETWEEN 'K' AND 'Z' THEN 'North' ELSE 'Unknown' END`

// This mirrors domain.BorrowableCount, including single-copy circulation.
const borrowableExpression = `(SELECT CASE
 WHEN count(*) FILTER (WHERE c.loan_policy = 'circulating' AND c.status IN ('available','on_loan')) < 2
 THEN count(*) FILTER (WHERE c.loan_policy = 'circulating' AND c.status = 'available')
 ELSE greatest(count(*) FILTER (WHERE c.loan_policy = 'circulating' AND c.status = 'available') - 1, 0)
 END FROM copies c WHERE c.book_id = b.id)`

const searchWhere = ` WHERE b.status = 'active'
 AND ($1 = '' OR b.search_vector @@ plainto_tsquery('english', $1))
 AND ($2 = '' OR b.title ILIKE '%' || $2 || '%')
 AND ($3 = '' OR EXISTS (SELECT 1 FROM book_authors ba JOIN authors a ON a.id = ba.author_id WHERE ba.book_id = b.id AND a.name ILIKE '%' || $3 || '%'))
 AND ($4 = '' OR EXISTS (SELECT 1 FROM book_subjects bs JOIN subjects s ON s.id = bs.subject_id WHERE bs.book_id = b.id AND s.heading ILIKE '%' || $4 || '%'))
 AND ($5 = '' OR b.isbn13 = $5 OR b.isbn10 = $5)
 AND ($6 = '' OR b.call_number ILIKE $6 || '%')
 AND ($7 = '' OR b.lcc_class = $7)
 AND ($8::boolean IS NULL OR (` + borrowableExpression + ` > 0) = $8)
 AND ($9::boolean IS NULL OR (` + borrowableExpression + ` > 0) = $9)
 AND ($10 = '' OR lower(b.faculty) = lower($10))
 AND ($11 = '' OR lower(b.department) = lower($11))
 AND ($12 = '' OR lower(b.language) = lower($12))
 AND ($13 = '' OR ` + wingExpression + ` = $13)
 AND ($14::integer IS NULL OR b.published_year >= $14)
 AND ($15::integer IS NULL OR b.published_year <= $15)`

// Search binds every input. The count and page share one snapshot, including
// pages beyond the last row, which must still report the matching total.
func (r *CatalogueRepo) Search(ctx context.Context, p SearchParams) ([]domain.Book, int, error) {
	available := p.Available
	if available == nil && p.OnlyAvailable {
		v := true
		available = &v
	}
	args := []any{p.Query, p.Title, p.Author, p.Subject, strings.ReplaceAll(p.ISBN, "-", ""), p.CallNumber, p.LCCClass, available, p.Borrowable, p.Faculty, p.Department, p.Language, p.Wing, p.YearFrom, p.YearTo}
	order := ` ORDER BY CASE WHEN $16 = 'relevance' THEN ts_rank(b.search_vector, plainto_tsquery('english', $1)) END DESC,
 CASE WHEN $16 = 'newest' THEN b.published_year END DESC NULLS LAST,
 CASE WHEN $16 = 'oldest' THEN b.published_year END ASC NULLS LAST, b.title, b.id LIMIT $17 OFFSET $18`
	sort := p.Sort
	if sort == "" {
		sort = "relevance"
	}
	return r.bookPage(ctx, searchWhere, order, args, []any{sort, p.Limit, p.Offset})
}

func (r *CatalogueRepo) bookPage(ctx context.Context, where, order string, filters, pagination []any) ([]domain.Book, int, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, translate(err)
	}
	defer tx.Rollback(ctx)
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*)`+bookFrom+where, filters...).Scan(&total); err != nil {
		return nil, 0, translate(err)
	}
	args := append(append([]any{}, filters...), pagination...)
	rows, err := tx.Query(ctx, bookSelect+where+order, args...)
	if err != nil {
		return nil, 0, translate(err)
	}
	books := make([]domain.Book, 0)
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			rows.Close()
			return nil, 0, translate(err)
		}
		books = append(books, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, translate(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, translate(err)
	}
	return books, total, nil
}

func (r *CatalogueRepo) NewArrivals(ctx context.Context, limit, offset int) ([]domain.Book, int, error) {
	return r.bookPage(ctx, ` WHERE b.status = 'active'`, ` ORDER BY b.created_at DESC,b.id LIMIT $1 OFFSET $2`, nil, []any{limit, offset})
}

func (r *CatalogueRepo) Related(ctx context.Context, id uuid.UUID, limit int) ([]domain.Book, error) {
	const subjects = `(SELECT count(*) FROM book_subjects candidate JOIN book_subjects source ON source.subject_id = candidate.subject_id WHERE candidate.book_id = b.id AND source.book_id = $1)`
	const authors = `(SELECT count(*) FROM book_authors candidate JOIN book_authors source ON source.author_id = candidate.author_id WHERE candidate.book_id = b.id AND source.book_id = $1)`
	rows, err := r.db.Query(ctx, bookSelect+` WHERE b.status = 'active' AND b.id <> $1
 AND EXISTS (SELECT 1 FROM books source WHERE source.id = $1 AND source.status = 'active')
 AND (`+subjects+` > 0 OR `+authors+` > 0)
 ORDER BY `+subjects+` DESC,`+authors+` DESC,b.title,b.id LIMIT $2`, id, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := make([]domain.Book, 0)
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, translate(err)
		}
		out = append(out, b)
	}
	return out, translate(rows.Err())
}

// Facets count public titles rather than copies or member affiliation records.
func (r *CatalogueRepo) Facets(ctx context.Context) (map[string][]domain.FacetValue, error) {
	const q = `WITH public_books AS (SELECT * FROM books WHERE status = 'active'), facet_values AS (
 SELECT 'subjects' AS facet,s.heading AS value,b.id FROM public_books b JOIN book_subjects bs ON bs.book_id=b.id JOIN subjects s ON s.id=bs.subject_id
 UNION ALL SELECT 'authors',a.name,b.id FROM public_books b JOIN book_authors ba ON ba.book_id=b.id JOIN authors a ON a.id=ba.author_id
 UNION ALL SELECT 'languages',b.language,b.id FROM public_books b
 UNION ALL SELECT 'publication_years',b.published_year::text,b.id FROM public_books b
 UNION ALL SELECT 'wings',` + wingExpression + `,b.id FROM public_books b
 UNION ALL SELECT 'faculties',b.faculty,b.id FROM public_books b
 UNION ALL SELECT 'departments',b.department,b.id FROM public_books b)
 SELECT facet,value,count(DISTINCT id) FROM facet_values WHERE value IS NOT NULL AND trim(value) <> '' GROUP BY facet,value ORDER BY facet,value`
	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := map[string][]domain.FacetValue{}
	for _, key := range []string{"subjects", "authors", "languages", "publication_years", "wings", "faculties", "departments"} {
		out[key] = []domain.FacetValue{}
	}
	for rows.Next() {
		var key string
		var v domain.FacetValue
		if err := rows.Scan(&key, &v.Value, &v.Count); err != nil {
			return nil, translate(err)
		}
		out[key] = append(out[key], v)
	}
	return out, translate(rows.Err())
}

func (r *CatalogueRepo) FindBook(ctx context.Context, id uuid.UUID) (domain.Book, error) {
	b, err := scanBook(r.db.QueryRow(ctx, bookSelect+` WHERE b.id = $1`, id))
	return b, translate(err)
}

// CreateBookParams carries a new bibliographic record.
type CreateBookParams struct {
	Edition            string
	Language           string
	Faculty            string
	Department         string
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
	// StaffID is the librarian adding the title, for the audit trail. Zero
	// when the importer adds a book from Open Library with nobody at a desk.
	StaffID uuid.UUID
}

// FindBookByISBN resolves a title already in the catalogue.
//
// Used when an import meets a work the library already holds, so the copies go
// to the existing title instead of creating a second one.
func (r *CatalogueRepo) FindBookByISBN(ctx context.Context, isbn string) (domain.Book, error) {
	isbn = strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(isbn))
	if isbn == "" {
		return domain.Book{}, domain.ErrNotFound
	}
	b, err := scanBook(r.db.QueryRow(ctx, bookSelect+` WHERE b.isbn13 = $1`, isbn))
	return b, translate(err)
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
		                   lcc_class, description, edition, language, faculty, department)
		VALUES ($1,$2,nullif($3,''),nullif($4,''),nullif($5,''),nullif($6,''),
		        $7,$8,$9,nullif($10,''),nullif($11,''),nullif($12,''),nullif($13,''),nullif($14,''))
		RETURNING id`,
		p.Title, nullif(p.Subtitle), p.ISBN13, p.ISBN10, p.Publisher,
		p.PlaceOfPublication, p.PublishedYear, strings.TrimSpace(p.CallNumber),
		lccClass, p.Description, p.Edition, p.Language, p.Faculty, p.Department).Scan(&id)
	if err != nil {
		// One ISBN is one title. A second attempt to catalogue the same work is
		// not an error to swallow: the caller should add a copy to the title
		// that already exists, which is what a librarian receiving a second
		// physical volume actually does (DOM-002, DEF-028).
		if isUniqueViolation(err, "books_isbn13_unique") {
			return domain.Book{}, domain.ErrDuplicateISBN
		}
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

	if err := recordAudit(ctx, tx, p.StaffID, "BOOK_CREATED", "book", id, map[string]any{
		"title":       p.Title,
		"call_number": p.CallNumber,
	}); err != nil {
		return domain.Book{}, err
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
func (r *CatalogueRepo) ArchiveBook(ctx context.Context, id, staffID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	tag, err := tx.Exec(ctx,
		`UPDATE books SET status = 'archived', updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if err := recordAudit(ctx, tx, staffID, "BOOK_ARCHIVED", "book", id, nil); err != nil {
		return err
	}
	return translate(tx.Commit(ctx))
}

// AddCopy registers one physical volume against a title.
//
// The accession number is the library's own per-item identifier and is unique
// across the whole collection; the call number it inherits from the book is not
// (DOM-002, REQ-022, REQ-023).
func (r *CatalogueRepo) AddCopy(ctx context.Context, bookID uuid.UUID,
	accession string, policy domain.LoanPolicy, staffID uuid.UUID) (domain.Copy, error) {

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Copy{}, translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	const q = `INSERT INTO copies (book_id, accession_number, loan_policy)
	           VALUES ($1,$2,$3)
	           RETURNING id, book_id, accession_number, loan_policy, status,
	                     acquired_at, coalesce(notes,'')`

	var c domain.Copy
	err = tx.QueryRow(ctx, q, bookID, strings.TrimSpace(accession), policy).Scan(
		&c.ID, &c.BookID, &c.AccessionNumber, &c.LoanPolicy, &c.Status,
		&c.AcquiredAt, &c.Notes)
	if err != nil {
		if isUniqueViolation(err, "copies_accession_number_key") {
			return domain.Copy{}, domain.ErrDuplicateAccession
		}
		return domain.Copy{}, translate(err)
	}

	if err := recordAudit(ctx, tx, staffID, "COPY_ADDED", "copy", c.ID, map[string]any{
		"book_id":          bookID,
		"accession_number": c.AccessionNumber,
		"loan_policy":      c.LoanPolicy,
	}); err != nil {
		return domain.Copy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
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

// FindCopy reads one physical volume.
func (r *CatalogueRepo) FindCopy(ctx context.Context, id uuid.UUID) (domain.Copy, error) {
	const q = `SELECT id, book_id, accession_number, loan_policy, status,
	                  acquired_at, coalesce(notes,'')
	             FROM copies WHERE id = $1`

	var c domain.Copy
	err := r.db.QueryRow(ctx, q, id).Scan(&c.ID, &c.BookID, &c.AccessionNumber,
		&c.LoanPolicy, &c.Status, &c.AcquiredAt, &c.Notes)
	return c, translate(err)
}

// CopyAtDesk is one physical volume as the circulation desk needs to see it:
// the volume, what it is a copy of, and whoever currently has it.
//
// The desk works from the barcode on the book and nothing else. Both desk
// screens begin the same way, so both begin with this one read rather than
// making the browser stitch three requests together and guess at the join.
type CopyAtDesk struct {
	Copy       domain.Copy `json:"copy"`
	BookTitle  string      `json:"book_title"`
	CallNumber string      `json:"call_number"`

	// The open loan, when the copy is out. Nil when it is on the shelf, which
	// is the difference between "this can be issued" and "this can be taken
	// back", and the only thing either screen has to decide.
	Loan *domain.Loan `json:"loan"`
}

// FindCopyByAccession resolves the number printed on the volume.
//
// The accession number is unique across the whole collection (DOM-002), which
// is what makes it usable as the desk's only input. The call number is not: a
// title's five copies share one.
func (r *CatalogueRepo) FindCopyByAccession(ctx context.Context, accession string) (CopyAtDesk, error) {
	const q = `
		SELECT c.id, c.book_id, c.accession_number, c.loan_policy, c.status,
		       c.acquired_at, coalesce(c.notes,''),
		       b.title, b.call_number,
		       l.id, l.user_id, l.borrowed_at, l.due_at, u.full_name
		  FROM copies c
		  JOIN books b ON b.id = c.book_id
		  -- At most one row can match: a partial unique index on
		  -- loans(copy_id) WHERE returned_at IS NULL makes a second open loan
		  -- for a copy unstorable.
		  LEFT JOIN loans l ON l.copy_id = c.id AND l.returned_at IS NULL
		  LEFT JOIN users u ON u.id = l.user_id
		 WHERE upper(trim(c.accession_number)) = upper(trim($1))`

	var d CopyAtDesk
	var loanID, memberID *uuid.UUID
	var borrowedAt, dueAt *time.Time
	var memberName *string

	err := r.db.QueryRow(ctx, q, accession).Scan(
		&d.Copy.ID, &d.Copy.BookID, &d.Copy.AccessionNumber, &d.Copy.LoanPolicy,
		&d.Copy.Status, &d.Copy.AcquiredAt, &d.Copy.Notes,
		&d.BookTitle, &d.CallNumber,
		&loanID, &memberID, &borrowedAt, &dueAt, &memberName)
	if err != nil {
		return CopyAtDesk{}, translate(err)
	}

	if loanID != nil {
		d.Loan = &domain.Loan{
			ID: *loanID, CopyID: d.Copy.ID, UserID: *memberID,
			BorrowedAt: *borrowedAt, DueAt: *dueAt,
			BookTitle: d.BookTitle, AccessionNumber: d.Copy.AccessionNumber,
		}
		if memberName != nil {
			d.Loan.MemberName = *memberName
		}
	}
	return d, nil
}

// SetCopyStatusClosingLoan records a copy as lost or damaged while it was out,
// and closes the loan in the same transaction.
//
// Both halves must happen together. A copy marked lost with its loan left open
// would show as permanently overdue; a loan closed without the copy's status
// changing would put a lost book back on the shelf. The loan is closed with the
// copy's new status recorded rather than as a return, so the history says what
// actually happened (DEF-009, DOM-008).
func (r *CatalogueRepo) SetCopyStatusClosingLoan(ctx context.Context, id uuid.UUID,
	status domain.CopyStatus, staffID uuid.UUID) error {

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(ctx, `
		UPDATE loans SET returned_at = now(), returned_to = $2
		 WHERE copy_id = $1 AND returned_at IS NULL`, id, staffID); err != nil {
		return translate(err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE copies SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log (actor_id, action, entity_type, entity_id, metadata)
		VALUES ($1, $2, 'copy', $3, jsonb_build_object('closed_open_loan', true))`,
		staffID, "COPY_MARKED_"+string(status), id); err != nil {
		return translate(err)
	}

	return translate(tx.Commit(ctx))
}

// UpdateCopy changes a volume's policy or status, which is how a librarian marks
// an item lost, damaged or withdrawn without erasing its history (REQ-024..026).
func (r *CatalogueRepo) UpdateCopy(ctx context.Context, id uuid.UUID,
	policy *domain.LoanPolicy, status *domain.CopyStatus, staffID uuid.UUID) error {

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	const q = `UPDATE copies
	              SET loan_policy = coalesce($2, loan_policy),
	                  status      = coalesce($3, status),
	                  updated_at  = now()
	            WHERE id = $1`

	tag, err := tx.Exec(ctx, q, id, policy, status)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	// Only the fields that were actually given are recorded. A nil here means
	// "left alone", and writing it down as a change would be a lie in the log.
	changed := map[string]any{}
	if policy != nil {
		changed["loan_policy"] = *policy
	}
	if status != nil {
		changed["status"] = *status
	}
	if err := recordAudit(ctx, tx, staffID, "COPY_UPDATED", "copy", id, changed); err != nil {
		return err
	}
	return translate(tx.Commit(ctx))
}

func nullif(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
