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
	"net/url"
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
//
// Both forms are produced. HTML carries the button a reader expects; the plain
// text is what a screen reader, a text-only client, or a spam filter reads, and
// a message with no text part is more likely to be treated as spam. Neither is
// a fallback for the other: they are the same message twice.
type Rendered struct {
	Subject string
	Body    string
	HTML    string
}

// FrontendURL is where email links point. Set once at startup.
//
// A package-level value rather than a parameter threaded through every template
// call: it is fixed for the life of the process, and passing it everywhere would
// obscure the templates without making anything safer.
var FrontendURL = "https://library.appmd.dev"

// SetFrontendURL configures the address used in email links.
func SetFrontendURL(u string) {
	if u != "" {
		FrontendURL = strings.TrimRight(u, "/")
	}
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
					"time you sign in.\n\n%s\n\nHezekiah Oluwasanmi Library",
				name, FrontendURL),
			HTML: emailHTML("Your library account",
				fmt.Sprintf("Dear %s,", name),
				"<p>Your library account is ready. Sign in with your matriculation "+
					"number or your university email, and the temporary password you "+
					"were given at the desk.</p>",
				button(FrontendURL, "Sign in"),
				"<p style=\"color:#5f6b62;font-size:13px\">You will be asked to choose "+
					"your own password the first time you sign in. Until you do, that is "+
					"the only thing your account can do.</p>"),
		}

	case "password_reset":
		token := valueOr(m.Payload, "token", "")
		// The token is a URL query parameter, so it is escaped rather than
		// concatenated. It is base64url today and would survive naive
		// concatenation, but a link that breaks the first time the token format
		// changes is a link nobody can debug from an inbox.
		link := FrontendURL + "/reset-password?token=" + url.QueryEscape(token)

		return Rendered{
			Subject: "Reset your library password",
			Body: fmt.Sprintf(
				"Dear %s,\n\nOpen this link to set a new password:\n\n    %s\n\n"+
					"The link can be used once and expires in 30 minutes.\n\n"+
					"If the link does not work, you can enter this code on the "+
					"reset page instead:\n\n    %s\n\n"+
					"If you did not ask for this, ignore this message and your "+
					"password will not change.\n\nHezekiah Oluwasanmi Library",
				name, link, token),
			HTML: emailHTML(
				"Reset your library password",
				fmt.Sprintf("Dear %s,", name),
				"<p>Use the button below to set a new password. It can be used once "+
					"and expires in 30 minutes.</p>",
				button(link, "Set a new password"),
				"<p style=\"color:#5f6b62;font-size:13px\">If the button does not work, "+
					"paste this code on the reset page:</p>"+
					"<p style=\"font-family:ui-monospace,SFMono-Regular,Menlo,monospace;"+
					"font-size:13px;background:#f2f5f3;padding:12px 14px;border-radius:6px;"+
					"word-break:break-all\">"+htmlEscape(token)+"</p>"+
					"<p style=\"color:#5f6b62;font-size:13px\">If you did not ask for this, "+
					"ignore this message and your password will not change.</p>",
			),
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
			HTML: emailHTML("Due soon",
				fmt.Sprintf("Dear %s,", name),
				fmt.Sprintf("<p><strong>%s</strong> is due back on <strong>%s</strong>.</p>",
					htmlEscape(title), htmlEscape(lagos(m.Payload, "due_at"))),
				"<p>Please return it to the Loans desk by then.</p>",
				button(FrontendURL+"/my-loans", "See your loans")),
		}

	case "loan_overdue":
		return Rendered{
			Subject: "Overdue: " + title,
			Body: fmt.Sprintf(
				"Dear %s,\n\n%s was due back on %s and has not been returned.\n\n"+
					"Please bring it to the Loans desk. Other readers are "+
					"waiting for it.\n\nHezekiah Oluwasanmi Library",
				name, title, lagos(m.Payload, "due_at")),
			HTML: emailHTML("Overdue",
				fmt.Sprintf("Dear %s,", name),
				fmt.Sprintf("<p><strong>%s</strong> was due back on <strong>%s</strong> "+
					"and has not been returned.</p>",
					htmlEscape(title), htmlEscape(lagos(m.Payload, "due_at"))),
				"<p>Please bring it to the Loans desk. Other readers are waiting for it.</p>",
				button(FrontendURL+"/my-loans", "See your loans")),
		}

	case "reservation_ready":
		return Rendered{
			Subject: "Ready to collect: " + title,
			Body: fmt.Sprintf(
				"Dear %s,\n\nA copy of %s is being held for you at the Loans "+
					"desk.\n\nPlease collect it by %s. After that it passes to "+
					"the next reader waiting.\n\nHezekiah Oluwasanmi Library",
				name, title, lagos(m.Payload, "expires_at")),
			HTML: emailHTML("Ready to collect",
				fmt.Sprintf("Dear %s,", name),
				fmt.Sprintf("<p>A copy of <strong>%s</strong> is being held for you at "+
					"the Loans desk.</p>", htmlEscape(title)),
				fmt.Sprintf("<p>Please collect it by <strong>%s</strong>. After that it "+
					"passes to the next reader waiting.</p>",
					htmlEscape(lagos(m.Payload, "expires_at"))),
				button(FrontendURL+"/my-reservations", "See your reservations")),
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

// --- html -------------------------------------------------------------------

// emailHTML wraps content in a plain, table-free layout.
//
// Deliberately simple: inline styles only, no external stylesheet, no web
// fonts, no images. Mail clients strip most of what a browser would accept, and
// an email that depends on a remote asset shows a broken box to anyone whose
// client blocks remote content, which is most of them by default.
//
// The contract for parts, which is easy to get wrong in both directions:
//
//   - A part beginning with "<" is treated as markup and inserted as given.
//     Anything interpolated into it MUST be escaped by the caller.
//   - Any other part is treated as plain text and escaped here. The caller must
//     NOT escape it first, or the reader sees "&lt;" in their inbox.
func emailHTML(title string, parts ...string) string {
	var body strings.Builder
	for _, p := range parts {
		if strings.HasPrefix(p, "<") {
			body.WriteString(p)
		} else {
			body.WriteString("<p>" + htmlEscape(p) + "</p>")
		}
	}

	return `<!doctype html><html><body style="margin:0;padding:0;background:#f4f6f5">
<div style="max-width:520px;margin:0 auto;padding:28px 20px;
     font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;
     font-size:15px;line-height:1.55;color:#1c2620">
  <div style="background:#14532d;color:#ffffff;padding:18px 22px;border-radius:8px 8px 0 0">
    <div style="font-weight:600;font-size:17px">Hezekiah Oluwasanmi Library</div>
    <div style="font-size:13px;color:#b9dcc6">Obafemi Awolowo University</div>
  </div>
  <div style="background:#ffffff;padding:24px 22px;border-radius:0 0 8px 8px;
       border:1px solid #e2e8e4;border-top:none">
    ` + body.String() + `
  </div>
  <div style="color:#7c8a81;font-size:12px;padding:14px 4px">
    This message was sent by the library management system. Please do not reply to it.
  </div>
</div></body></html>`
}

// button renders a call to action.
//
// An anchor styled as a button rather than an actual button element: a <button>
// outside a form does nothing in an email, and several clients drop it entirely.
func button(href, label string) string {
	return `<p style="margin:24px 0"><a href="` + htmlEscape(href) + `"
     style="display:inline-block;background:#14532d;color:#ffffff;
     text-decoration:none;padding:12px 22px;border-radius:6px;
     font-weight:600;font-size:15px">` + htmlEscape(label) + `</a></p>`
}

// htmlEscape prevents a name or a token from breaking out of the markup.
//
// A member's full name comes from a CSV a librarian uploaded, so it is not
// trustworthy input: a name containing a tag would otherwise be rendered by the
// mail client.
func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;",
	).Replace(s)
}
