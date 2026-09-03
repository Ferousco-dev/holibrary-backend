package notify

import (
	"context"
	"log/slog"
)

// Console "delivers" a message by writing it to the log.
//
// It exists so the notification pipeline can be run and demonstrated without a
// mail account: the outbox, the state re-check, the retry accounting and the
// worker all behave exactly as they will in production, and only the final hop
// changes. Without it a development stack has no delivery channel at all, so
// the worker idles and nothing can be observed.
//
// Enabled only outside production, and only when no real provider is
// configured. It never runs alongside Resend, so a configured system cannot
// accidentally log a member's reset token instead of emailing it.
type Console struct{ channel string }

func NewConsole(channel string) *Console { return &Console{channel: channel} }

func (c *Console) Channel() string { return c.channel }

func (c *Console) Send(_ context.Context, m Message) error {
	rendered := Render(m)

	// The recipient and subject are logged; the body is not. A password reset
	// body carries a working token, and NFR-010 keeps secrets out of the logs
	// even in development, where logs are most casually shared.
	slog.Info("notification delivered to the console",
		"channel", c.channel,
		"to", m.To,
		"template", m.Template,
		"subject", rendered.Subject,
	)
	return nil
}
