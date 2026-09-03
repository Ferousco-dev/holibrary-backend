package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditRepo records who changed what, and when (NFR-020, REQ-068).
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
	CreatedAt  string         `json:"created_at"`
}

// Record writes an audit row. Failures are returned but callers generally log
// and continue: losing an audit line is bad, failing the member's request
// because of it is worse.
func (r *AuditRepo) Record(ctx context.Context, actorID uuid.UUID, action, entityType string, entityID uuid.UUID, metadata map[string]any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO audit_log (actor_id, action, entity_type, entity_id, metadata)
		VALUES ($1,$2,$3,$4,$5)`, actorID, action, entityType, entityID, encoded)
	return translate(err)
}

func (r *AuditRepo) List(ctx context.Context, limit, offset int) ([]AuditEntry, int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.id, a.actor_id, coalesce(u.full_name,'(deleted)'), a.action,
		       a.entity_type, a.entity_id, a.metadata,
		       to_char(a.created_at, 'YYYY-MM-DD"T"HH24:MI:SSZ'),
		       count(*) OVER() AS total
		  FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id
		 ORDER BY a.created_at DESC
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
