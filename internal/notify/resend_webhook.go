package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// VerifyResendWebhook validates Svix signatures used by Resend webhooks.
// Verification covers the exact raw body and rejects stale/replayed events.
func VerifyResendWebhook(secret string, headers http.Header, body []byte, now time.Time) error {
	if secret == "" {
		return fmt.Errorf("webhook secret is not configured")
	}
	id := headers.Get("svix-id")
	timestamp := headers.Get("svix-timestamp")
	signatures := headers.Get("svix-signature")
	if id == "" || timestamp == "" || signatures == "" {
		return fmt.Errorf("missing webhook signature headers")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || absDuration(now.Sub(time.Unix(seconds, 0))) > 5*time.Minute {
		return fmt.Errorf("stale webhook timestamp")
	}
	secret = strings.TrimPrefix(secret, "whsec_")
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return fmt.Errorf("invalid webhook secret")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(id + "." + timestamp + "."))
	_, _ = mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, value := range strings.Fields(signatures) {
		parts := strings.SplitN(value, ",", 2)
		if len(parts) == 2 && parts[0] == "v1" && hmac.Equal([]byte(expected), []byte(parts[1])) {
			return nil
		}
	}
	return fmt.Errorf("invalid webhook signature")
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
