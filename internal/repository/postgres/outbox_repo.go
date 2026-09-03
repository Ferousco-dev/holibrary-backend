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

// Pending claims messages that are due for delivery.
//
// FOR UPDATE SKIP LOCKED means two workers, or two container instances, never
// pick up the same row: each takes what the other has not locked. Without it a
// restart overlapping with the previous process would send everything twice.
//
// scheduled_at gates the claim, so a reminder written today for next week waits.
func (r *OutboxRepo) Pending(ctx context.Context, limit int) ([]PendingMessage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT o.id, o.user_id, u.email, u.full_name, o.channel, o.template, o.payload
		  FROM outbox o JOIN users u ON u.id = o.user_id
		 WHERE o.id IN (
		     SELECT id FROM outbox
		      WHERE status = 'pending' AND attempts < 5 AND scheduled_at <= now()
		      ORDER BY scheduled_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT $1)
		 ORDER BY o.scheduled_at`, limit)
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

// StillRelevant re-checks the world before a message goes out.
//
// A reminder queued last night describes a situation that may have changed:
//
//	"Your book is due tomorrow"  -> queued
//	member returns the book       -> five minutes later
//	worker runs                   -> must not send it
//
// The queue records an intention; only the database knows whether the intention
// still holds. Checked at send time, not at queue time.
func (r *OutboxRepo) StillRelevant(ctx context.Context, template string, payload map[string]any) (bool, error) {
	id, ok := payload["loan_id"].(string)

	switch template {
	case "loan_due_soon", "loan_overdue":
		if !ok {
			return true, nil // nothing to check against; send it
		}
		var open bool
		err := r.db.QueryRow(ctx,
			`SELECT returned_at IS NULL FROM loans WHERE id = $1`, id).Scan(&open)
		if err != nil {
			// The loan has gone entirely. Chasing a member over a record that
			// no longer exists would be worse than staying quiet.
			return false, nil
		}
		return open, nil

	case "reservation_ready":
		reservationID, ok := payload["reservation_id"].(string)
		if !ok {
			return true, nil
		}
		var status string
		err := r.db.QueryRow(ctx,
			`SELECT status FROM reservations WHERE id = $1`, reservationID).Scan(&status)
		if err != nil {
			return false, nil
		}
		// Cancelled or expired between queueing and sending: say nothing.
		return status == "ready", nil

	default:
		// Welcome messages, receipts and password resets describe something
		// that already happened and cannot become untrue.
		return true, nil
	}
}

// MarkSuperseded closes a message the world has moved past, distinctly from one
// that was delivered, so the log tells the truth about what happened.
func (r *OutboxRepo) MarkSuperseded(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE outbox SET status='superseded', sent_at=now(), last_error=$2 WHERE id=$1`,
		id, reason)
	return translate(err)
}

// DeviceTokens returns a member's live push registrations.
//
// A member may read from a phone, a laptop and a library terminal, so a push
// fans out to every registered device rather than to one stored token.
func (r *OutboxRepo) DeviceTokens(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT token FROM device_tokens WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, translate(err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeDeviceToken retires a registration FCM has rejected as permanently
// invalid, so a dead device is not retried forever.
func (r *OutboxRepo) RevokeDeviceToken(ctx context.Context, token string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE device_tokens SET revoked_at = now() WHERE token = $1 AND revoked_at IS NULL`,
		token)
	return translate(err)
}

// RegisterDevice records a push registration.
//
// The token is unique across the table, not per user: when one student signs
// out of a shared library terminal and another signs in, the row moves to the
// new owner rather than leaving the previous member receiving notifications
// about somebody else's books.
func (r *OutboxRepo) RegisterDevice(ctx context.Context, userID uuid.UUID, token, platform string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO device_tokens (user_id, token, platform)
		VALUES ($1, $2, $3)
		ON CONFLICT (token) DO UPDATE
		   SET user_id = EXCLUDED.user_id,
		       platform = EXCLUDED.platform,
		       last_seen_at = now(),
		       revoked_at = NULL`, userID, token, platform)
	return translate(err)
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
