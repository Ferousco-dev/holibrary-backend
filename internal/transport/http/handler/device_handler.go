package handler

import (
	"net/http"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

type DeviceHandler struct{ outbox *postgres.OutboxRepo }

func NewDeviceHandler(o *postgres.OutboxRepo) *DeviceHandler { return &DeviceHandler{outbox: o} }

type registerDeviceRequest struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

// Register records a device for push notifications (REQ-071).
//
// The owner is taken from the token. Registering a device against somebody
// else's account would send them a stranger's due dates, which is both a bug
// and a privacy leak.
func (h *DeviceHandler) Register(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	var req registerDeviceRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Token == "" {
		response.ValidationError(w, "token is required.", nil)
		return
	}
	if req.Platform == "" {
		req.Platform = "web"
	}

	if err := h.outbox.RegisterDevice(r.Context(), userID, req.Token, req.Platform); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "registered"}, nil)
}

// Unregister detaches a device on sign-out.
//
// Without this, a student who signs out of a shared library terminal keeps
// receiving notifications about their books on a machine somebody else is now
// using.
func (h *DeviceHandler) Unregister(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.UserID(r.Context()); !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	var req registerDeviceRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.outbox.RevokeDeviceToken(r.Context(), req.Token); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "unregistered"}, nil)
}
