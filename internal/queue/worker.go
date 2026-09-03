// Package queue drains the notification outbox.
//
// Nothing here runs on a request. A service writes an outbox row in the same
// transaction as the change that caused it, and this worker delivers it later,
// so a slow mail provider can never slow down the circulation desk
// (docs/design.md DES-008, REQ-072).
package queue

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/notify"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

// Outbox is the persistence the worker needs.
type Outbox interface {
	Pending(ctx context.Context, limit int) ([]postgres.PendingMessage, error)
	StillRelevant(ctx context.Context, template string, payload map[string]any) (bool, error)
	MarkSent(ctx context.Context, id uuid.UUID) error
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
	MarkSuperseded(ctx context.Context, id uuid.UUID, reason string) error
	DeviceTokens(ctx context.Context, userID uuid.UUID) ([]string, error)
	RevokeDeviceToken(ctx context.Context, token string) error
}

// Worker delivers queued notifications.
type Worker struct {
	outbox   Outbox
	senders  map[string]notify.Sender
	interval time.Duration
	batch    int
}

// NewWorker builds a worker. A channel with no sender is left queued rather
// than failed, so nothing is lost while a provider is being configured.
func NewWorker(outbox Outbox, interval time.Duration, senders ...notify.Sender) *Worker {
	byChannel := make(map[string]notify.Sender, len(senders))
	for _, s := range senders {
		byChannel[s.Channel()] = s
	}
	return &Worker{outbox: outbox, senders: byChannel, interval: interval, batch: 20}
}

// Run drains the outbox until the context is cancelled.
//
// Started as a goroutine from main and stopped by the same signal that stops
// the server, so a deployment does not leave a batch half-sent.
func (w *Worker) Run(ctx context.Context) {
	if len(w.senders) == 0 {
		slog.Info("notification worker idle: no delivery channel is configured")
		return
	}

	channels := make([]string, 0, len(w.senders))
	for c := range w.senders {
		channels = append(channels, c)
	}
	slog.Info("notification worker started", "channels", channels, "interval", w.interval)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("notification worker stopped")
			return
		case <-ticker.C:
			if n, err := w.drainOnce(ctx); err != nil {
				// A failed pass is not fatal: the rows are still pending and
				// the next tick will try again.
				slog.Error("notification pass failed", "error", err)
			} else if n > 0 {
				slog.Info("notifications processed", "count", n)
			}
		}
	}
}

// drainOnce processes one batch and reports how many messages it handled.
func (w *Worker) drainOnce(ctx context.Context) (int, error) {
	messages, err := w.outbox.Pending(ctx, w.batch)
	if err != nil {
		return 0, err
	}

	handled := 0
	for _, m := range messages {
		if ctx.Err() != nil {
			return handled, nil // shutting down; the rest stay pending
		}
		w.deliver(ctx, m)
		handled++
	}
	return handled, nil
}

// deliver sends one message, after checking it is still worth sending.
func (w *Worker) deliver(ctx context.Context, m postgres.PendingMessage) {
	// The queue records an intention; only the database knows whether the
	// intention still holds. A member who returned a book five minutes after
	// the reminder was queued must not be chased for it.
	relevant, err := w.outbox.StillRelevant(ctx, m.Template, m.Payload)
	if err != nil {
		slog.Warn("could not re-check a queued notification",
			"outbox_id", m.ID, "template", m.Template, "error", err)
		return // leave it pending; the next pass will try again
	}
	if !relevant {
		_ = w.outbox.MarkSuperseded(ctx, m.ID, "the situation changed before delivery")
		slog.Info("notification superseded before sending",
			"outbox_id", m.ID, "template", m.Template)
		return
	}

	sender, ok := w.senders[m.Channel]
	if !ok {
		// No provider for this channel yet. Leaving it pending means the
		// message arrives once the provider is configured, rather than being
		// silently lost.
		return
	}

	if m.Channel == "push" {
		w.deliverPush(ctx, m, sender)
		return
	}

	msg := notify.Message{To: m.Email, Name: m.FullName, Template: m.Template, Payload: m.Payload}
	if err := sender.Send(ctx, msg); err != nil {
		w.recordFailure(ctx, m, err)
		return
	}
	if err := w.outbox.MarkSent(ctx, m.ID); err != nil {
		// The message went out but the record did not update. Saying so is
		// better than a log that quietly disagrees with reality.
		slog.Error("delivered but could not mark as sent",
			"outbox_id", m.ID, "error", err)
	}
}

// deliverPush fans a push message out to every device the member has registered.
//
// A member may read the catalogue from a phone, a laptop and a library terminal.
// Sending to one stored token would reach whichever device happened to register
// last.
func (w *Worker) deliverPush(ctx context.Context, m postgres.PendingMessage, sender notify.Sender) {
	tokens, err := w.outbox.DeviceTokens(ctx, m.UserID)
	if err != nil {
		slog.Warn("could not read device tokens", "user_id", m.UserID, "error", err)
		return
	}
	if len(tokens) == 0 {
		// Never registered a device, or signed out everywhere. There is nothing
		// to deliver and nothing to retry.
		_ = w.outbox.MarkSuperseded(ctx, m.ID, "the member has no registered device")
		return
	}

	var lastErr error
	delivered := 0
	for _, token := range tokens {
		err := sender.Send(ctx, notify.Message{
			To: token, Name: m.FullName, Template: m.Template, Payload: m.Payload,
		})
		switch {
		case err == nil:
			delivered++
		case errors.Is(err, notify.ErrPermanent):
			// The app was uninstalled or the token rotated. The device is gone;
			// retrying it forever is how a queue fills with corpses.
			if revokeErr := w.outbox.RevokeDeviceToken(ctx, token); revokeErr != nil {
				slog.Warn("could not retire a dead device token", "error", revokeErr)
			}
		default:
			lastErr = err
		}
	}

	// One reachable device is a delivered notification. Failing the message
	// because a second, dead device rejected it would resend to the first.
	if delivered > 0 {
		if err := w.outbox.MarkSent(ctx, m.ID); err != nil {
			slog.Error("pushed but could not mark as sent", "outbox_id", m.ID, "error", err)
		}
		return
	}
	if lastErr != nil {
		w.recordFailure(ctx, m, lastErr)
		return
	}
	// Every token was permanently invalid: there is no device left to reach.
	_ = w.outbox.MarkSuperseded(ctx, m.ID, "every registered device was rejected")
}

// recordFailure counts the attempt. MarkFailed gives up after five, so a
// permanently bad address does not spin forever.
func (w *Worker) recordFailure(ctx context.Context, m postgres.PendingMessage, cause error) {
	reason := cause.Error()
	if errors.Is(cause, notify.ErrPermanent) {
		// No amount of retrying fixes a malformed address.
		_ = w.outbox.MarkSuperseded(ctx, m.ID, reason)
		slog.Warn("notification permanently undeliverable",
			"outbox_id", m.ID, "template", m.Template, "error", reason)
		return
	}
	if err := w.outbox.MarkFailed(ctx, m.ID, reason); err != nil {
		slog.Error("could not record a delivery failure", "outbox_id", m.ID, "error", err)
	}
	slog.Warn("notification delivery failed, will retry",
		"outbox_id", m.ID, "template", m.Template, "error", reason)
}
