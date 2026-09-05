package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditRepo records who changed what, and when (NFR-020, REQ-068).
//
// created_at is read as a timestamptz and marshalled by encoding/json as
// RFC 3339. An earlier version formatted it in SQL with to_char and appended a
// literal "Z", which labelled a Lagos-local time as UTC and silently shifted
// every audit entry by an hour. DEF-004.
type AuditRepo struct{ db *pgxpool.Pool }

func NewAuditRepo(db *pgxpool.Pool) *AuditRepo { return &AuditRepo{db: db} }

type AuditEntry struct {
	ID         uuid.UUID      `json:"id"`
	ActorID    *uuid.UUID     `json:"actor_id"`
	ActorName  string         `json:"actor_name"`
	Action     string         `json:"action"`
	EntityType string         `json:"entity_type"`
	EntityID   *uuid.UUID     `json:"entity_id"`
	Metadata   map[string]any `json:"metadata"`
	// time.Time, not a preformatted string: encoding/json renders it as
	// RFC 3339 and the driver hands it over already resolved to an instant.
	CreatedAt time.Time `json:"created_at"`
}

func (r *AuditRepo) List(ctx context.Context, limit, offset int) ([]AuditEntry, int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.id, a.actor_id, coalesce(u.full_name,'(deleted)'), a.action,
		       a.entity_type, a.entity_id, a.metadata,
		       a.created_at,
		       count(*) OVER() AS total
		  FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id
		 ORDER BY a.created_at DESC, a.id
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	var out []AuditEntry
	total := 0
	for rows.Next() {
		var e AuditEntry
		var metadata []byte
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.Action,
			&e.EntityType, &e.EntityID, &metadata, &e.CreatedAt, &total); err != nil {
			return nil, 0, translate(err)
		}
		_ = json.Unmarshal(metadata, &e.Metadata)
		out = append(out, e)
	}
	return out, total, rows.Err()
}
