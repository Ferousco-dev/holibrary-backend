package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/notify"
	"github.com/Ferousco-dev/holibrary-backend/internal/queue"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

type fakeOutbox struct {
	pending    []postgres.PendingMessage
	relevant   bool
	sent       []uuid.UUID
	failed     []string
	superseded []string
	tokens     []string
	revoked    []string
}

func (f *fakeOutbox) Pending(context.Context, int) ([]postgres.PendingMessage, error) {
	out := f.pending
	f.pending = nil // one pass only, so a test cannot loop
	return out, nil
}
func (f *fakeOutbox) StillRelevant(context.Context, string, map[string]any) (bool, error) {
	return f.relevant, nil
}
func (f *fakeOutbox) MarkSent(_ context.Context, id uuid.UUID) error {
	f.sent = append(f.sent, id)
	return nil
}
func (f *fakeOutbox) MarkFailed(_ context.Context, _ uuid.UUID, reason string) error {
	f.failed = append(f.failed, reason)
	return nil
}
func (f *fakeOutbox) MarkSuperseded(_ context.Context, _ uuid.UUID, reason string) error {
	f.superseded = append(f.superseded, reason)
	return nil
}
func (f *fakeOutbox) DeviceTokens(context.Context, uuid.UUID) ([]string, error) {
	return f.tokens, nil
}
func (f *fakeOutbox) RevokeDeviceToken(_ context.Context, token string) error {
	f.revoked = append(f.revoked, token)
	return nil
}

type fakeSender struct {
	channel string
	sentTo  []string
	err     error
	errFor  map[string]error
}

func (f *fakeSender) Channel() string { return f.channel }
func (f *fakeSender) Send(_ context.Context, m notify.Message) error {
	if e, ok := f.errFor[m.To]; ok {
		return e
	}
	if f.err != nil {
		return f.err
	}
	f.sentTo = append(f.sentTo, m.To)
	return nil
}

func message(channel, template string) postgres.PendingMessage {
	return postgres.PendingMessage{
		ID: uuid.New(), UserID: uuid.New(), Email: "member@oauife.edu.ng",
		FullName: "Ada Obi", Channel: channel, Template: template,
		Payload: map[string]any{"title": "Clean Code"},
	}
}

// run drains one pass. The worker ticks, so the test lets exactly one tick fire.
func run(t *testing.T, w *queue.Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	w.Run(ctx)
}

func TestWorkerDeliversAndMarksSent(t *testing.T) {
	outbox := &fakeOutbox{pending: []postgres.PendingMessage{message("email", "loan_due_soon")}, relevant: true}
	sender := &fakeSender{channel: "email"}

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(sender.sentTo) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sender.sentTo))
	}
	if len(outbox.sent) != 1 {
		t.Errorf("marked %d as sent, want 1", len(outbox.sent))
	}
}

// The bug this exists to prevent:
//
//	"your book is due tomorrow"  -> queued
//	member returns the book       -> five minutes later
//	worker runs                   -> must stay quiet
//
// The queue records an intention; only the database knows whether it still holds.
func TestWorkerDoesNotSendAMessageTheWorldHasMovedPast(t *testing.T) {
	outbox := &fakeOutbox{pending: []postgres.PendingMessage{message("email", "loan_due_soon")}, relevant: false}
	sender := &fakeSender{channel: "email"}

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(sender.sentTo) != 0 {
		t.Error("a reminder for a book already returned must not be sent")
	}
	if len(outbox.superseded) != 1 {
		t.Errorf("the message should be recorded as superseded, got %v", outbox.superseded)
	}
	if len(outbox.sent) != 0 {
		t.Error("it must not be recorded as delivered")
	}
}

// A transient failure is retried; the row stays pending and the attempt counts.
func TestWorkerRetriesATransientFailure(t *testing.T) {
	outbox := &fakeOutbox{pending: []postgres.PendingMessage{message("email", "welcome")}, relevant: true}
	sender := &fakeSender{channel: "email", err: errors.New("provider timed out")}

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(outbox.failed) != 1 {
		t.Errorf("the attempt should be recorded for retry, got %v", outbox.failed)
	}
	if len(outbox.superseded) != 0 {
		t.Error("a transient failure must not be given up on")
	}
}

// A malformed address will not improve on its own. Retrying it four more times
// only delays the queue.
func TestWorkerGivesUpOnAPermanentFailure(t *testing.T) {
	outbox := &fakeOutbox{pending: []postgres.PendingMessage{message("email", "welcome")}, relevant: true}
	sender := &fakeSender{channel: "email",
		err: errors.New("Resend returned 422: invalid recipient: " + notify.ErrPermanent.Error())}
	sender.err = notify.ErrPermanent

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(outbox.superseded) != 1 {
		t.Errorf("a permanent failure should be closed, not retried: %v", outbox.superseded)
	}
	if len(outbox.failed) != 0 {
		t.Error("it must not be queued for another attempt")
	}
}

// A member reads from a phone, a laptop and a library terminal. One stored
// token would reach whichever registered last.
func TestPushFansOutToEveryRegisteredDevice(t *testing.T) {
	outbox := &fakeOutbox{
		pending:  []postgres.PendingMessage{message("push", "reservation_ready")},
		relevant: true,
		tokens:   []string{"phone-token", "laptop-token", "terminal-token"},
	}
	sender := &fakeSender{channel: "push"}

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(sender.sentTo) != 3 {
		t.Errorf("pushed to %d devices, want 3", len(sender.sentTo))
	}
	if len(outbox.sent) != 1 {
		t.Error("the message is one notification, however many devices received it")
	}
}

// A dead device must be retired, not retried forever — and it must not stop the
// member's working device from being told.
func TestPushRetiresADeadDeviceAndStillDelivers(t *testing.T) {
	outbox := &fakeOutbox{
		pending:  []postgres.PendingMessage{message("push", "reservation_ready")},
		relevant: true,
		tokens:   []string{"dead-token", "live-token"},
	}
	sender := &fakeSender{channel: "push", errFor: map[string]error{"dead-token": notify.ErrPermanent}}

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(outbox.revoked) != 1 || outbox.revoked[0] != "dead-token" {
		t.Errorf("the dead token should be retired, got %v", outbox.revoked)
	}
	if len(sender.sentTo) != 1 || sender.sentTo[0] != "live-token" {
		t.Errorf("the working device should still receive it, got %v", sender.sentTo)
	}
	if len(outbox.sent) != 1 {
		t.Error("one reachable device means the notification was delivered")
	}
}

// A member who never registered a device has nothing to receive a push. That is
// not a failure to retry.
func TestPushWithNoRegisteredDeviceIsClosedNotRetried(t *testing.T) {
	outbox := &fakeOutbox{
		pending:  []postgres.PendingMessage{message("push", "reservation_ready")},
		relevant: true,
	}
	sender := &fakeSender{channel: "push"}

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(outbox.superseded) != 1 {
		t.Errorf("it should be closed, got superseded=%v failed=%v", outbox.superseded, outbox.failed)
	}
}

// A channel with no provider leaves its messages queued, so nothing is lost
// while an account is being set up.
func TestMessagesForAnUnconfiguredChannelStayQueued(t *testing.T) {
	outbox := &fakeOutbox{pending: []postgres.PendingMessage{message("push", "reservation_ready")}, relevant: true}
	sender := &fakeSender{channel: "email"} // no push provider

	run(t, queue.NewWorker(outbox, 20*time.Millisecond, sender))

	if len(outbox.sent)+len(outbox.failed)+len(outbox.superseded) != 0 {
		t.Error("an undeliverable channel should leave the message pending, not resolve it")
	}
}
