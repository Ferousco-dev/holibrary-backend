package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/notify"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

// ResendWebhookHandler accepts only verified Resend/Svix events.
type ResendWebhookHandler struct {
	invitations *postgres.InvitationRepo
	secret      string
}

func NewResendWebhookHandler(invitations *postgres.InvitationRepo, secret string) *ResendWebhookHandler {
	return &ResendWebhookHandler{invitations: invitations, secret: secret}
}

type resendEvent struct {
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Data      struct {
		EmailID       string `json:"email_id"`
		Reason        string `json:"reason"`
		FailureReason string `json:"failure_reason"`
	} `json:"data"`
}

func (h *ResendWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 1<<20 {
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return
	}
	if err := notify.VerifyResendWebhook(h.secret, r.Header, body, time.Now().UTC()); err != nil {
		http.Error(w, "invalid webhook", http.StatusUnauthorized)
		return
	}
	var event resendEvent
	if err := json.Unmarshal(body, &event); err != nil || event.Data.EmailID == "" {
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return
	}
	occurredAt := time.Now().UTC()
	if parsed, parseErr := time.Parse(time.RFC3339, event.CreatedAt); parseErr == nil {
		occurredAt = parsed
	}
	reason := event.Data.Reason
	if reason == "" {
		reason = event.Data.FailureReason
	}
	if err := h.invitations.ApplyEvent(r.Context(), event.Data.EmailID, event.Type, occurredAt, reason); err != nil {
		http.Error(w, "webhook unavailable", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
