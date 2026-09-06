-- Track password-setup invitation delivery separately from the outbox payload.
-- The setup token remains only in the outbox payload and is never selected here.
CREATE TABLE invitation_deliveries (
    id                  uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    member_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email               citext NOT NULL,
    outbox_id           uuid NOT NULL UNIQUE REFERENCES outbox(id) ON DELETE CASCADE,
    import_batch_id     uuid,
    resend_message_id   text UNIQUE,
    queued_at           timestamptz NOT NULL DEFAULT now(),
    sent_at             timestamptz,
    delivered_at        timestamptz,
    opened_at           timestamptz,
    bounced_at          timestamptz,
    failure_reason      text,
    status              text NOT NULL DEFAULT 'queued',
    CONSTRAINT invitation_delivery_status CHECK
        (status IN ('queued', 'sent', 'delivered', 'opened', 'bounced', 'complained', 'failed', 'superseded'))
);

CREATE INDEX invitation_deliveries_member_idx
    ON invitation_deliveries (member_id, queued_at DESC);
CREATE INDEX invitation_deliveries_status_idx
    ON invitation_deliveries (status, queued_at DESC);
CREATE INDEX invitation_deliveries_batch_idx
    ON invitation_deliveries (import_batch_id, queued_at DESC);
