package notify

import (
	"context"
	"encoding/base64"
	"testing"
)

func TestFCMTokenSourceRejectsBadKeys(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"not JSON and not base64", "this is not a key"},
		{"valid base64, not JSON", base64.StdEncoding.EncodeToString([]byte("nope"))},
		{"JSON but wrong type", `{"type":"authorized_user","project_id":"x"}`},
		{"service account with no project", `{"type":"service_account"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := FCMTokenSource(context.Background(), c.key); err == nil {
				t.Fatalf("accepted a key it should have refused")
			}
		})
	}
}

// An unset variable is not an error. Push is optional, and the caller decides
// whether its absence matters; returning an error here would stop the server
// booting over a feature nobody had configured yet.
func TestFCMTokenSourceTreatsEmptyAsUnconfigured(t *testing.T) {
	for _, empty := range []string{"", "   ", "\n"} {
		project, token, err := FCMTokenSource(context.Background(), empty)
		if err != nil {
			t.Fatalf("empty key returned an error: %v", err)
		}
		if project != "" || token != nil {
			t.Fatalf("empty key produced a configured sender")
		}
	}
}
