package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InvitationDelivery is safe for admin reporting: it contains delivery
// metadata only, never the password-setup token.
type InvitationDelivery struct {
	ID              uuid.UUID  `json:"id"`
	MemberID        uuid.UUID  `json:"member_id"`
	Email           string     `json:"email"`
	OutboxID        uuid.UUID  `json:"outbox_id"`
	ImportBatchID   *uuid.UUID `json:"import_batch_id,omitempty"`
	ResendMessageID string     `json:"resend_message_id,omitempty"`
	QueuedAt        time.Time  `json:"queued_at"`
	SentAt          *time.Time `json:"sent_at,omitempty"`
	DeliveredAt     *time.Time `json:"delivered_at,omitempty"`
	OpenedAt        *time.Time `json:"opened_at,omitempty"`
	BouncedAt       *time.Time `json:"bounced_at,omitempty"`
	FailureReason   string     `json:"failure_reason,omitempty"`
	Status          string     `json:"status"`
}

type InvitationRepo struct{ db *pgxpool.Pool }

func NewInvitationRepo(db *pgxpool.Pool) *InvitationRepo { return &InvitationRepo{db: db} }

func (r *InvitationRepo) CreateForOutbox(ctx context.Context, tx pgx.Tx, userID, outboxID uuid.UUID, email string, batchID *uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO invitation_deliveries (member_id, email, outbox_id, import_batch_id)
		VALUES ($1, $2, $3, $4)`, userID, email, outboxID, batchID)
	return err
}

// UpdateProviderID records the ID returned by Resend. It is deliberately
// separate from MarkSent so a provider ID can be retained before webhooks.
func (r *InvitationRepo) UpdateProviderID(ctx context.Context, outboxID uuid.UUID, providerID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE invitation_deliveries SET resend_message_id = $2, sent_at = coalesce(sent_at, now()), status = 'sent'
		 WHERE outbox_id = $1`, outboxID, providerID)
	return translate(err)
}

func (r *InvitationRepo) MarkFailed(ctx context.Context, outboxID uuid.UUID, reason string) error {
	_, err := r.db.Exec(ctx, `UPDATE invitation_deliveries SET status = 'failed', failure_reason = nullif($2, '') WHERE outbox_id = $1`, outboxID, reason)
	return translate(err)
}

func (r *InvitationRepo) MarkSuperseded(ctx context.Context, outboxID uuid.UUID, reason string) error {
	_, err := r.db.Exec(ctx, `UPDATE invitation_deliveries SET status = 'superseded', failure_reason = nullif($2, '') WHERE outbox_id = $1`, outboxID, reason)
	return translate(err)
}

func (r *InvitationRepo) InvalidateForMember(ctx context.Context, memberID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		UPDATE invitation_deliveries
		   SET status = 'superseded', failure_reason = 'replaced by a newer invitation'
		 WHERE member_id = $1 AND status IN ('queued', 'sent', 'delivered', 'opened')`, memberID)
	return translate(err)
}

// ApplyEvent is idempotent and monotonic: a late sent event cannot move a
// delivered or bounced invitation backwards.
func (r *InvitationRepo) ApplyEvent(ctx context.Context, providerID, eventType string, occurredAt time.Time, reason string) error {
	status := ""
	column := ""
	switch eventType {
	case "email.sent":
		status, column = "sent", "sent_at"
	case "email.delivered":
		status, column = "delivered", "delivered_at"
	case "email.opened":
		status, column = "opened", "opened_at"
	case "email.bounced":
		status, column = "bounced", "bounced_at"
	case "email.complained":
		status = "complained"
	case "email.failed":
		status = "failed"
	default:
		return nil
	}
	if column != "" {
		_, err := r.db.Exec(ctx, `
			UPDATE invitation_deliveries
			   SET status = CASE WHEN status IN ('bounced','complained','failed') THEN status ELSE $2 END,
			       `+column+` = coalesce(`+column+`, $3),
			       failure_reason = CASE WHEN $2 IN ('bounced','failed') THEN nullif($4, '') ELSE failure_reason END
			 WHERE resend_message_id = $1`, providerID, status, occurredAt, reason)
		return translate(err)
	}
	_, err := r.db.Exec(ctx, `
		UPDATE invitation_deliveries SET status = $2, failure_reason = nullif($3, '')
		 WHERE resend_message_id = $1 AND status NOT IN ('bounced','complained','failed')`, providerID, status, reason)
	return translate(err)
}

func (r *InvitationRepo) List(ctx context.Context, status string, batchID *uuid.UUID, limit, offset int) ([]InvitationDelivery, int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, member_id, email, outbox_id, import_batch_id, coalesce(resend_message_id,''),
		       queued_at, sent_at, delivered_at, opened_at, bounced_at, coalesce(failure_reason,''), status,
		       count(*) OVER()
		  FROM invitation_deliveries
		 WHERE ($1 = '' OR status = $1)
		   AND ($2::uuid IS NULL OR import_batch_id = $2)
		 ORDER BY queued_at DESC, id
		 LIMIT $3 OFFSET $4`, status, batchID, limit, offset)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	var out []InvitationDelivery
	total := 0
	for rows.Next() {
		var item InvitationDelivery
		if err := rows.Scan(&item.ID, &item.MemberID, &item.Email, &item.OutboxID, &item.ImportBatchID,
			&item.ResendMessageID, &item.QueuedAt, &item.SentAt, &item.DeliveredAt, &item.OpenedAt,
			&item.BouncedAt, &item.FailureReason, &item.Status, &total); err != nil {
			return nil, 0, translate(err)
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func (r *InvitationRepo) Summary(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx, `SELECT status, count(*) FROM invitation_deliveries GROUP BY status`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	result := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, translate(err)
		}
		result[status] = count
	}
	return result, rows.Err()
}
