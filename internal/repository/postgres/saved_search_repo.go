package postgres

import (
	"context"
	"encoding/json"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SavedSearchRepo struct{ db *pgxpool.Pool }

func NewSavedSearchRepo(db *pgxpool.Pool) *SavedSearchRepo { return &SavedSearchRepo{db: db} }
func (r *SavedSearchRepo) Create(ctx context.Context, userID uuid.UUID, name string, query map[string]json.RawMessage, limit int) (domain.SavedSearch, error) {
	var out domain.SavedSearch
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return out, translate(err)
	}
	defer tx.Rollback(ctx)
	// A member row serializes concurrent creates even when there are no saved searches.
	var lockedID uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&lockedID); err != nil {
		return out, translate(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM saved_searches WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return out, translate(err)
	}
	if count >= limit {
		return out, domain.ErrSavedSearchLimit
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO saved_searches(user_id,name,query) VALUES($1,$2,$3) RETURNING id,user_id,name,query,created_at`, userID, name, encoded).Scan(&out.ID, &out.UserID, &out.Name, &out.Query, &out.CreatedAt)
	if err != nil {
		return out, translate(err)
	}
	if err = recordAudit(ctx, tx, userID, "SAVED_SEARCH_CREATED", "saved_search", out.ID, nil); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, translate(err)
	}
	return out, nil
}
func (r *SavedSearchRepo) ListForUser(ctx context.Context, userID uuid.UUID) ([]domain.SavedSearch, error) {
	rows, err := r.db.Query(ctx, `SELECT id,user_id,name,query,created_at FROM saved_searches WHERE user_id=$1 ORDER BY created_at DESC,id`, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := make([]domain.SavedSearch, 0)
	for rows.Next() {
		var search domain.SavedSearch
		if err = rows.Scan(&search.ID, &search.UserID, &search.Name, &search.Query, &search.CreatedAt); err != nil {
			return nil, translate(err)
		}
		out = append(out, search)
	}
	return out, translate(rows.Err())
}
func (r *SavedSearchRepo) Delete(ctx context.Context, id, userID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return translate(err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM saved_searches WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if err = recordAudit(ctx, tx, userID, "SAVED_SEARCH_DELETED", "saved_search", id, nil); err != nil {
		return err
	}
	return translate(tx.Commit(ctx))
}
