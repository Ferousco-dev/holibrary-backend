package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxRepo implements the transactional outbox.
//
// A notification is a row in the same database as the change that caused it, so
// a due-date reminder cannot be queued for a loan that was rolled back. A worker
// drains the table and talks to Resend and FCM; the request path never does
// (docs/design.md DES-008, REQ-072).
type OutboxRepo struct{ db *pgxpool.Pool }

func NewOutboxRepo(db *pgxpool.Pool) *OutboxRepo { return &OutboxRepo{db: db} }

func (r *OutboxRepo) Queue(ctx context.Context, userID uuid.UUID, channel, template string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		`INSERT INTO outbox (user_id, channel, template, payload) VALUES ($1,$2,$3,$4)`,
		userID, channel, template, encoded)
	return translate(err)
}

// Pending returns messages awaiting delivery, oldest first.
type PendingMessage struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	Email    string
	FullName string
	Channel  string
	Template string
	Payload  map[string]any
}

func (r *OutboxRepo) Pending(ctx context.Context, limit int) ([]PendingMessage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT o.id, o.user_id, u.email, u.full_name, o.channel, o.template, o.payload
		  FROM outbox o JOIN users u ON u.id = o.user_id
		 WHERE o.status = 'pending' AND o.attempts < 5
		 ORDER BY o.created_at
		 LIMIT $1`, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []PendingMessage
	for rows.Next() {
		var m PendingMessage
		var payload []byte
		if err := rows.Scan(&m.ID, &m.UserID, &m.Email, &m.FullName,
			&m.Channel, &m.Template, &payload); err != nil {
			return nil, translate(err)
		}
		_ = json.Unmarshal(payload, &m.Payload)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *OutboxRepo) MarkSent(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`UPDATE outbox SET status='sent', sent_at=now() WHERE id=$1`, id)
	return translate(err)
}

// MarkFailed records the attempt and the reason. Pending() stops retrying after
// five attempts so a permanently bad address does not spin forever.
func (r *OutboxRepo) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE outbox
		   SET attempts = attempts + 1,
		       last_error = $2,
		       status = CASE WHEN attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END
		 WHERE id = $1`, id, reason)
	return translate(err)
}
