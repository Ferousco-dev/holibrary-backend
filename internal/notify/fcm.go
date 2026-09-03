package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FCM sends push notifications through Firebase Cloud Messaging.
//
// Firebase is in this system for push and nothing else. Crashlytics and
// Performance Monitoring are client-side products with no Go server SDK; they
// cannot observe this API (DEC-017).
type FCM struct {
	projectID string
	token     func(context.Context) (string, error)
	client    *http.Client
}

// NewFCM builds a sender. The token function supplies an OAuth2 access token
// for the service account; it is injected rather than built here so the sender
// can be tested without Google credentials.
func NewFCM(projectID string, token func(context.Context) (string, error)) *FCM {
	return &FCM{
		projectID: projectID,
		token:     token,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (f *FCM) Channel() string { return "push" }

func (f *FCM) Configured() bool { return f.projectID != "" && f.token != nil }

func (f *FCM) Send(ctx context.Context, m Message) error {
	rendered := Render(m)

	accessToken, err := f.token(ctx)
	if err != nil {
		return fmt.Errorf("obtaining an FCM access token: %w", err)
	}

	// FCM v1 rejects non-string data values, so the payload is flattened.
	data := map[string]string{"template": m.Template}
	for k, v := range m.Payload {
		if s, ok := v.(string); ok {
			data[k] = s
		}
	}

	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": m.To,
			"notification": map[string]string{
				"title": rendered.Subject,
				// A push is a glance, not a letter: the first line only.
				"body": firstLine(rendered.Body),
			},
			"data": data,
		},
	})
	if err != nil {
		return fmt.Errorf("%w: encoding message: %v", ErrPermanent, err)
	}

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", f.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling FCM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		// UNREGISTERED means the app was uninstalled or the token rotated. The
		// device is gone; retrying it forever is how a queue fills with corpses.
		if strings.Contains(string(payload), "UNREGISTERED") ||
			strings.Contains(string(payload), "INVALID_ARGUMENT") {
			return fmt.Errorf("%w: FCM rejected the device token: %s",
				ErrPermanent, strings.TrimSpace(string(payload)))
		}
		return describe("FCM", resp.StatusCode, string(payload))
	}
	return nil
}

func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "Dear ") {
			return line
		}
	}
	return "You have a message from the library."
}
