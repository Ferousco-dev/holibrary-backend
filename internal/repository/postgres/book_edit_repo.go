package postgres

import (
	"context"
	"slices"
	"strings"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
)

// UpdateBookParams distinguishes omitted fields from explicitly empty values.
// Input validation and normalization belong to the catalogue service.
type UpdateBookParams struct {
	Title, Subtitle, ISBN13, ISBN10, Publisher, PlaceOfPublication  *string
	CallNumber, Edition, Language, Description, Faculty, Department *string
	PublishedYear                                                   *int
	Authors, Subjects                                               *[]string
	StaffID                                                         uuid.UUID
}

// UpdateBook serializes edits to a bibliographic record, preserving omitted
// fields and all circulation records. An identical patch is a no-op: it leaves
// updated_at unchanged and creates no audit entry.
func (r *CatalogueRepo) UpdateBook(ctx context.Context, id uuid.UUID, p UpdateBookParams) (domain.Book, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Book{}, translate(err)
	}
	defer tx.Rollback(ctx)
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM books WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
		return domain.Book{}, translate(err)
	}
	before, err := scanBook(tx.QueryRow(ctx, bookSelect+` WHERE b.id=$1`, id))
	if err != nil {
		return domain.Book{}, translate(err)
	}
	changed := make([]string, 0)
	for _, field := range []struct {
		name     string
		supplied *string
		previous string
	}{
		{"title", p.Title, before.Title}, {"subtitle", p.Subtitle, before.Subtitle},
		{"isbn13", p.ISBN13, before.ISBN13}, {"isbn10", p.ISBN10, before.ISBN10},
		{"publisher", p.Publisher, before.Publisher}, {"place_of_publication", p.PlaceOfPublication, before.PlaceOfPublication},
		{"call_number", p.CallNumber, before.CallNumber}, {"edition", p.Edition, before.Edition},
		{"language", p.Language, before.Language}, {"description", p.Description, before.Description},
		{"faculty", p.Faculty, before.Faculty}, {"department", p.Department, before.Department},
	} {
		if field.supplied != nil && *field.supplied != field.previous {
			changed = append(changed, field.name)
		}
	}
	if p.PublishedYear != nil && (before.PublishedYear == nil || *p.PublishedYear != *before.PublishedYear) {
		changed = append(changed, "published_year")
	}
	if p.Authors != nil && !slices.Equal(*p.Authors, before.Authors) {
		changed = append(changed, "authors")
	}
	if p.Subjects != nil {
		subjects := slices.Clone(*p.Subjects)
		slices.Sort(subjects)
		previous := slices.Clone(before.Subjects)
		slices.Sort(previous)
		if !slices.Equal(subjects, previous) {
			changed = append(changed, "subjects")
		}
	}
	if len(changed) == 0 {
		return before, translate(tx.Commit(ctx))
	}
	var lcc *string
	if p.CallNumber != nil {
		class := strings.ToUpper(strings.TrimSpace(*p.CallNumber))
		if class == "" {
			return domain.Book{}, domain.ErrInvalidCallNumber
		}
		class = class[:1]
		lcc = &class
	}
	_, err = tx.Exec(ctx, `UPDATE books SET
 title=coalesce($2,title), subtitle=CASE WHEN $3::text IS NULL THEN subtitle ELSE nullif($3,'') END,
 isbn13=CASE WHEN $4::text IS NULL THEN isbn13 ELSE nullif($4,'') END,
 isbn10=CASE WHEN $5::text IS NULL THEN isbn10 ELSE nullif($5,'') END,
 publisher=CASE WHEN $6::text IS NULL THEN publisher ELSE nullif($6,'') END,
 place_of_publication=CASE WHEN $7::text IS NULL THEN place_of_publication ELSE nullif($7,'') END,
 published_year=coalesce($8,published_year), call_number=coalesce($9,call_number), lcc_class=coalesce($10,lcc_class),
 edition=CASE WHEN $11::text IS NULL THEN edition ELSE nullif($11,'') END,
 language=CASE WHEN $12::text IS NULL THEN language ELSE nullif($12,'') END,
 description=CASE WHEN $13::text IS NULL THEN description ELSE nullif($13,'') END,
 faculty=CASE WHEN $14::text IS NULL THEN faculty ELSE nullif($14,'') END,
 department=CASE WHEN $15::text IS NULL THEN department ELSE nullif($15,'') END,
 updated_at=now() WHERE id=$1`, id, p.Title, p.Subtitle, p.ISBN13, p.ISBN10, p.Publisher, p.PlaceOfPublication, p.PublishedYear, p.CallNumber, lcc, p.Edition, p.Language, p.Description, p.Faculty, p.Department)
	if err != nil {
		if isUniqueViolation(err, "books_isbn13_unique") {
			return domain.Book{}, domain.ErrDuplicateISBN
		}
		return domain.Book{}, translate(err)
	}
	if slices.Contains(changed, "authors") {
		if _, err := tx.Exec(ctx, `DELETE FROM book_authors WHERE book_id=$1`, id); err != nil {
			return domain.Book{}, translate(err)
		}
		if err := linkAuthors(ctx, tx, id, *p.Authors); err != nil {
			return domain.Book{}, err
		}
	}
	if slices.Contains(changed, "subjects") {
		if _, err := tx.Exec(ctx, `DELETE FROM book_subjects WHERE book_id=$1`, id); err != nil {
			return domain.Book{}, translate(err)
		}
		if err := linkSubjects(ctx, tx, id, *p.Subjects); err != nil {
			return domain.Book{}, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT books_refresh_search_vector($1)`, id); err != nil {
		return domain.Book{}, translate(err)
	}
	slices.Sort(changed)
	if err := recordAudit(ctx, tx, p.StaffID, "BOOK_METADATA_UPDATED", "book", id, map[string]any{"changed_fields": changed}); err != nil {
		return domain.Book{}, err
	}
	updated, err := scanBook(tx.QueryRow(ctx, bookSelect+` WHERE b.id=$1`, id))
	if err != nil {
		return domain.Book{}, translate(err)
	}
	return updated, translate(tx.Commit(ctx))
}
