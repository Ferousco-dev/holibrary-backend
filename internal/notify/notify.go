// Package notify delivers messages to members by email and push.
//
// Delivery never happens on the request path. The services write an outbox row
// in the same transaction as the change that caused it, and the worker in
// internal/queue drains that table. This package is only the transport
// (docs/design.md DES-008, REQ-069..072).
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Message is one notification ready to send.
type Message struct {
	To       string // email address, or an FCM registration token
	Name     string
	Template string
	Payload  map[string]any
}

// ErrPermanent marks a failure that retrying cannot fix: a malformed address, a
// deregistered device. The worker stops retrying these immediately instead of
// burning attempts on something that will never succeed.
var ErrPermanent = errors.New("permanent delivery failure")

// Sender delivers a message over one channel.
type Sender interface {
	Send(ctx context.Context, m Message) error
	Channel() string
}

// Rendered is the finished text of a message.
type Rendered struct {
	Subject string
	Body    string
}

// Render turns a template name and payload into text.
//
// Templates are Go string building rather than html/template files: there are
// six of them, they are plain text, and a template directory would be another
// thing to keep in step with the binary. Times are shown in Africa/Lagos,
// because a due date is the one thing a member must not misread -- the stored
// value stays UTC.
func Render(m Message) Rendered {
	name := valueOr(m.Payload, "full_name", m.Name)
	title := valueOr(m.Payload, "title", "your book")

	switch m.Template {
	case "welcome":
		return Rendered{
			Subject: "Your Hezekiah Oluwasanmi Library account",
			Body: fmt.Sprintf(
				"Dear %s,\n\nYour library account is ready. Sign in with your "+
					"matriculation number or your university email and the "+
					"temporary password you were given at the desk.\n\n"+
					"You will be asked to choose your own password the first "+
					"time you sign in.\n\nHezekiah Oluwasanmi Library",
				name),
		}

	case "password_reset":
		return Rendered{
			Subject: "Reset your library password",
			Body: fmt.Sprintf(
				"Dear %s,\n\nUse this code to set a new password:\n\n    %s\n\n"+
					"It can be used once and expires in 30 minutes. If you did "+
					"not ask for it, you can ignore this message and your "+
					"password will not change.\n\nHezekiah Oluwasanmi Library",
				name, valueOr(m.Payload, "token", "")),
		}

	case "loan_receipt":
		return Rendered{
			Subject: "Book borrowed: " + title,
			Body: fmt.Sprintf(
				"Dear %s,\n\nYou borrowed %s.\n\nPlease return it by %s.\n\n"+
					"Hezekiah Oluwasanmi Library",
				name, title, lagos(m.Payload, "due_at")),
		}

	case "loan_due_soon":
		return Rendered{
			Subject: "Due soon: " + title,
			Body: fmt.Sprintf(
				"Dear %s,\n\n%s is due back on %s.\n\nPlease return it to the "+
					"Loans desk by then.\n\nHezekiah Oluwasanmi Library",
				name, title, lagos(m.Payload, "due_at")),
		}

	case "loan_overdue":
		return Rendered{
			Subject: "Overdue: " + title,
			Body: fmt.Sprintf(
				"Dear %s,\n\n%s was due back on %s and has not been returned.\n\n"+
					"Please bring it to the Loans desk. Other readers are "+
					"waiting for it.\n\nHezekiah Oluwasanmi Library",
				name, title, lagos(m.Payload, "due_at")),
		}

	case "reservation_ready":
		return Rendered{
			Subject: "Ready to collect: " + title,
			Body: fmt.Sprintf(
				"Dear %s,\n\nA copy of %s is being held for you at the Loans "+
					"desk.\n\nPlease collect it by %s. After that it passes to "+
					"the next reader waiting.\n\nHezekiah Oluwasanmi Library",
				name, title, lagos(m.Payload, "expires_at")),
		}

	default:
		// An unknown template is a programming error, not a member's problem.
		// Sending something plain beats sending nothing and losing the record.
		slog.Warn("no template for notification", "template", m.Template)
		return Rendered{
			Subject: "A message from Hezekiah Oluwasanmi Library",
			Body:    fmt.Sprintf("Dear %s,\n\nPlease check your library account.\n", name),
		}
	}
}

func valueOr(payload map[string]any, key, fallback string) string {
	if v, ok := payload[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return fallback
}

// lagos formats a stored UTC instant in the library's local time.
//
// The member reads "Thursday 17 September 2026, 7:15 pm", which is what the
// clock in the building will say. The stored value never leaves UTC.
func lagos(payload map[string]any, key string) string {
	raw := valueOr(payload, key, "")
	if raw == "" {
		return "the date on your account"
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		return t.UTC().Format("Monday 2 January 2006, 3:04 pm") + " UTC"
	}
	return t.In(loc).Format("Monday 2 January 2006, 3:04 pm")
}

// isPermanent reports whether an HTTP status means retrying is pointless.
func isPermanent(status int) bool {
	// 4xx other than 408 Request Timeout and 429 Too Many Requests describe the
	// request, and the request will not improve on its own.
	return status >= 400 && status < 500 && status != 408 && status != 429
}

func describe(provider string, status int, body string) error {
	body = strings.TrimSpace(body)
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	err := fmt.Errorf("%s returned %d: %s", provider, status, body)
	if isPermanent(status) {
		return fmt.Errorf("%w: %s", ErrPermanent, err)
	}
	return err
}
