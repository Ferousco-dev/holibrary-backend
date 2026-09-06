package handler

import (
	"net/http"
	"strings"

	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
	"github.com/google/uuid"
)

type AdminHandler struct {
	circulation *service.CirculationService
	audit       *postgres.AuditRepo
	invitations *postgres.InvitationRepo
	members     *service.MemberService
}

func NewAdminHandler(c *service.CirculationService, a *postgres.AuditRepo, invitations *postgres.InvitationRepo, members *service.MemberService) *AdminHandler {
	return &AdminHandler{circulation: c, audit: a, invitations: invitations, members: members}
}

// Dashboard returns the counts a librarian opens the day with (REQ-065).
func (h *AdminHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := h.circulation.Stats(r.Context())
	if err != nil {
		response.FromError(w, err)
		return
	}
	invitations, err := h.invitations.Summary(r.Context())
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"books": stats.Books, "copies": stats.Copies, "members": stats.Members,
		"active_loans": stats.ActiveLoans, "overdue": stats.Overdue,
		"invitation_statuses": invitations,
	}, nil)
}

// Audit returns the trail of staff actions (REQ-068, NFR-020).
//
// Administrator-only: the log records which librarian lent what to which named
// student, so it is more sensitive than the data it describes (DOM-009).
func (h *AdminHandler) Audit(w http.ResponseWriter, r *http.Request) {
	limit, offset, page := pagination(r)

	entries, total, err := h.audit.List(r.Context(), limit, offset)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, entries, &response.Meta{
		Page: page, PerPage: limit, Total: total,
	})
}

func (h *AdminHandler) InvitationDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, offset, page := pagination(r)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	var batchID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("batch_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			response.ValidationError(w, "batch_id must be a UUID", nil)
			return
		}
		batchID = &parsed
	}
	entries, total, err := h.invitations.List(r.Context(), status, batchID, limit, offset)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, entries, &response.Meta{Page: page, PerPage: limit, Total: total})
}

func (h *AdminHandler) ResendInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.members.ResendInvitation(r.Context(), id); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, map[string]string{"status": "invitation queued"}, nil)
}

// Health reports whether the service and its database are reachable (REQ-074).
//
// Render polls this, and it is also what tells you the free-tier container has
// woken up before a demo (RSK-001).
func Health(ping func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ping(); err != nil {
			response.Error(w, http.StatusServiceUnavailable, "DATABASE_UNREACHABLE",
				"The database is not reachable.", nil)
			return
		}
		response.JSON(w, http.StatusOK, map[string]string{
			"status":   "ok",
			"service":  "holibrary-backend",
			"database": "reachable",
		}, nil)
	}
}
