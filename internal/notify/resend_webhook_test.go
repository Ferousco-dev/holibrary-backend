package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestVerifyResendWebhookAcceptsValidSignature(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("webhook-secret"))
	body := []byte(`{"type":"email.delivered","data":{"email_id":"re_123"}}`)
	now := time.Unix(1700000000, 0).UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write([]byte("msg_123." + timestamp + "."))
	mac.Write(body)
	headers := http.Header{}
	headers.Set("svix-id", "msg_123")
	headers.Set("svix-timestamp", timestamp)
	headers.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	if err := VerifyResendWebhook(secret, headers, body, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyResendWebhookRejectsTamperingAndReplay(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("webhook-secret"))
	headers := http.Header{}
	headers.Set("svix-id", "msg_123")
	headers.Set("svix-timestamp", "1700000000")
	headers.Set("svix-signature", "v1,not-valid")
	if err := VerifyResendWebhook(secret, headers, []byte(`{}`), time.Unix(1700000000, 0).Add(6*time.Minute)); err == nil {
		t.Fatal("stale or invalid webhook must be rejected")
	}
}
