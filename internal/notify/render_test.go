package notify

import (
	"strings"
	"testing"
)

// The reset email must carry a link a member can click, not only a code to
// retype. A code is a fallback for when the link fails, not the primary path.
func TestPasswordResetCarriesALink(t *testing.T) {
	SetFrontendURL("https://library.appmd.dev")

	r := Render(Message{
		Template: "password_reset",
		Payload:  map[string]any{"full_name": "Ada Obi", "token": "abc123"},
	})

	want := "https://library.appmd.dev/reset-password?token=abc123"
	if !strings.Contains(r.HTML, want) {
		t.Errorf("the HTML body should link to %s", want)
	}
	if !strings.Contains(r.Body, want) {
		t.Errorf("the text body should contain %s, for clients that show no HTML", want)
	}
	// The code stays, because a link can be mangled by a mail client or a
	// corporate rewriter and the member still needs a way through.
	if !strings.Contains(r.Body, "abc123") {
		t.Error("the code should remain available as a fallback")
	}
	if !strings.Contains(r.HTML, "Set a new password") {
		t.Error("the HTML should present a labelled button")
	}
}

// A token is a URL parameter, so it must be escaped. Concatenation works today
// because the tokens are base64url, and would break silently the day that
// changes.
func TestResetLinkEscapesTheToken(t *testing.T) {
	SetFrontendURL("https://library.appmd.dev")

	r := Render(Message{
		Template: "password_reset",
		Payload:  map[string]any{"full_name": "Ada", "token": "a b&c=d"},
	})
	if strings.Contains(r.HTML, "token=a b&c=d") {
		t.Error("the token was placed in the URL without escaping")
	}
	if !strings.Contains(r.HTML, "a+b%26c%3Dd") {
		t.Errorf("expected an escaped token in the link, got: %s", r.HTML)
	}
}

// A member's name comes from a CSV a librarian uploaded, so it is not
// trustworthy input. A name containing markup must not reach the mail client
// as markup.
func TestNamesAreEscapedInHTML(t *testing.T) {
	r := Render(Message{
		Template: "welcome",
		Payload:  map[string]any{"full_name": `<script>alert(1)</script>`},
	})
	if strings.Contains(r.HTML, "<script>") {
		t.Error("a name containing a tag was rendered as markup")
	}
	if !strings.Contains(r.HTML, "&lt;script&gt;") {
		t.Error("the name should appear escaped")
	}
}

// Every template a member receives should produce both parts: a mail with no
// text alternative scores worse with spam filters and is unreadable to some
// clients.
func TestMemberFacingTemplatesHaveBothParts(t *testing.T) {
	for _, tmpl := range []string{
		"welcome", "password_reset", "loan_due_soon", "loan_overdue", "reservation_ready",
	} {
		r := Render(Message{
			Template: tmpl,
			Payload: map[string]any{
				"full_name": "Ada Obi", "title": "Clean Code", "token": "t",
				"due_at": "2026-09-17T18:15:00Z", "expires_at": "2026-09-06T18:15:00Z",
			},
		})
		if r.Subject == "" {
			t.Errorf("%s: no subject", tmpl)
		}
		if r.Body == "" {
			t.Errorf("%s: no plain-text part", tmpl)
		}
		if r.HTML == "" {
			t.Errorf("%s: no HTML part", tmpl)
		}
	}
}
