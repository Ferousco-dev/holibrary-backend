package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// execer is satisfied by both *pgxpool.Pool and pgx.Tx.
//
// That is the whole point of this file. An audit line has to be written by
// whichever of the two the caller already holds: writing it on the pool while
// the change it describes is still uncommitted in a transaction means the two
// can disagree, and the disagreement is permanent. If the transaction then
// rolls back, the log says a librarian did something the database has no
// record of.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// recordAudit writes one line of the audit trail (REQ-068, NFR-020).
//
// Pass the transaction when there is one, so the line and the change commit or
// roll back together. Pass the pool only when the change is a single statement
// that has already succeeded.
//
// actorID is uuid.Nil for an action with no signed-in actor behind it, which
// happens exactly once per deployment when cmd/bootstrap creates the first
// administrator. It is stored as NULL rather than as a zero uuid, because
// actor_id is a foreign key to users(id) and no user has that id.
func recordAudit(ctx context.Context, q execer, actorID uuid.UUID, action, entityType string,
	entityID uuid.UUID, metadata map[string]any) error {

	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}

	var actor *uuid.UUID
	if actorID != uuid.Nil {
		actor = &actorID
	}
	var entity *uuid.UUID
	if entityID != uuid.Nil {
		entity = &entityID
	}

	_, err = q.Exec(ctx, `
		INSERT INTO audit_log (actor_id, action, entity_type, entity_id, metadata)
		VALUES ($1,$2,$3,$4,$5)`, actor, action, entityType, entity, encoded)
	return translate(err)
}
