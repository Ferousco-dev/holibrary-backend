package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// fcmScope is the only permission this service account needs. Firebase issues
// keys that can do a great deal more; asking for one narrow scope means a
// leaked token cannot read the project's data or change its configuration.
const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCMTokenSource turns a Firebase service account key into the token function
// NewFCM expects.
//
// The key arrives as an environment variable rather than a file, because the
// container is built FROM scratch and has no filesystem to put one on. It may
// be given as raw JSON or base64-encoded: a multi-line JSON blob survives some
// dashboards badly, and base64 is the reliable way to paste one.
//
// Returns the project id alongside the token function. Both come out of the
// same key, so reading the project id from a second variable would let the two
// disagree, and a mismatch there fails at send time with a message that does
// not say why.
func FCMTokenSource(ctx context.Context, serviceAccountKey string) (string, func(context.Context) (string, error), error) {
	raw := strings.TrimSpace(serviceAccountKey)
	if raw == "" {
		return "", nil, nil // not configured; the caller decides whether that matters
	}

	// A JSON key starts with '{'. Anything else is assumed to be base64.
	if !strings.HasPrefix(raw, "{") {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return "", nil, fmt.Errorf("the Firebase key is neither JSON nor valid base64: %w", err)
		}
		raw = string(decoded)
	}

	var key struct {
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return "", nil, fmt.Errorf("the Firebase key is not valid JSON: %w", err)
	}
	if key.Type != "service_account" {
		return "", nil, fmt.Errorf("the Firebase key is of type %q, expected service_account", key.Type)
	}
	if key.ProjectID == "" {
		return "", nil, fmt.Errorf("the Firebase key has no project_id")
	}

	config, err := google.JWTConfigFromJSON([]byte(raw), fcmScope)
	if err != nil {
		return "", nil, fmt.Errorf("reading the Firebase service account key: %w", err)
	}

	// The token source caches and refreshes on its own, so this does not mint
	// a new token per notification.
	source := oauth2.ReuseTokenSource(nil, config.TokenSource(ctx))

	return key.ProjectID, func(context.Context) (string, error) {
		token, err := source.Token()
		if err != nil {
			return "", fmt.Errorf("obtaining a Firebase access token: %w", err)
		}
		return token.AccessToken, nil
	}, nil
}
