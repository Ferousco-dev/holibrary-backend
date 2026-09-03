package handler

import (
	"errors"
	"net/http"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

// maxImportBytes caps a member-roll upload. Eight megabytes is far more than a
// full intake needs as CSV, and small enough that a hostile upload cannot
// exhaust the container's memory.
const maxImportBytes = 8 << 20

type MemberHandler struct {
	members     *service.MemberService
	circulation *service.CirculationService
}

func NewMemberHandler(m *service.MemberService, c *service.CirculationService) *MemberHandler {
	return &MemberHandler{members: m, circulation: c}
}

// isKnownDomainError reports whether err is one the response package can map to
// a specific status and code.
func isKnownDomainError(err error) bool {
	for _, known := range []error{
		domain.ErrForbidden, domain.ErrConflict, domain.ErrNotFound,
		domain.ErrUnauthenticated, domain.ErrPasswordTooWeak, domain.ErrNoCategory,
	} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}

type createMemberRequest struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email"`
	FullName   string `json:"full_name"`
	// First and last name may be supplied separately, as they appear on the
	// identity card the applicant presents at the desk.
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// Faculty, department and level are captured at registration because the
	// librarian already has the card in hand, and they make the member roll
	// searchable the way a librarian thinks: "the 200-level Software
	// Engineering students". Columns added in migration 0003; the CSV import
	// accepted them from the start, and this endpoint did not.
	Faculty    string `json:"faculty"`
	Department string `json:"department"`
	Level      string `json:"level"`
	Role       string `json:"role"`
	Category   string `json:"category"`
}

// Create registers a member at the desk (REQ-009).
//
// The temporary password is returned once, for the librarian to hand over on
// paper. It is not stored in readable form and cannot be retrieved again.
func (h *MemberHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createMemberRequest
	if !decode(w, r, &req) {
		return
	}

	user, temporary, err := h.members.Create(r.Context(), middleware.Role(r.Context()), service.NewMemberParams{
		Identifier: req.Identifier,
		Email:      req.Email,
		FullName:   req.FullName,
		FirstName:  req.FirstName,
		LastName:   req.LastName,
		Faculty:    req.Faculty,
		Department: req.Department,
		Level:      req.Level,
		Category:   domain.MemberCategory(req.Category),
		Role:       domain.Role(req.Role),
	})
	if err != nil {
		// Known domain errors carry their own status: a librarian attempting to
		// create an admin is 403 FORBIDDEN, not a malformed request. Sniffing
		// the error's type to decide was wrong and reported a privilege
		// violation as a validation failure. DEF-010.
		if isKnownDomainError(err) {
			response.FromError(w, err)
			return
		}
		// What remains is a field-level complaint from validateNewMember. Those
		// messages describe the form, not the system, so they are safe to show.
		response.ValidationError(w, err.Error(), nil)
		return
	}

	response.JSON(w, http.StatusCreated, map[string]any{
		"member":             toUserResponse(user),
		"temporary_password": temporary,
		"note":               "Give this password to the member. They must change it at first sign-in.",
	}, nil)
}

// ImportCSV registers many members from a spreadsheet (REQ-010, REQ-011).
//
// Pass ?dry_run=true to validate without writing. The response has the same
// shape either way, so the librarian previews the file, fixes the rows the
// summary names, and re-posts to commit. Every row's outcome is reported and a
// malformed line is skipped rather than aborting the batch.
func (h *MemberHandler) ImportCSV(w http.ResponseWriter, r *http.Request) {
	// Cap the upload so one large file cannot exhaust a small container.
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
		response.ValidationError(w, "The upload could not be read. Maximum size is 8 MB.", nil)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		response.ValidationError(w, "Attach a CSV file in the 'file' field.", nil)
		return
	}
	defer file.Close()

	result, err := h.members.ImportCSV(r.Context(), middleware.Role(r.Context()), file, boolParam(r, "dry_run"))
	if err != nil {
		response.ValidationError(w, err.Error(), nil)
		return
	}
	response.JSON(w, http.StatusOK, result, nil)
}

// List returns members matching an optional search (REQ-012).
func (h *MemberHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page := pagination(r)

	members, total, err := h.members.List(r.Context(), r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		response.FromError(w, err)
		return
	}

	out := make([]userResponse, 0, len(members))
	for _, m := range members {
		out = append(out, toUserResponse(m))
	}
	response.JSON(w, http.StatusOK, out, &response.Meta{
		Page: page, PerPage: limit, Total: total,
	})
}

// Get returns a member with their borrowing record (REQ-013, REQ-063).
//
// Librarian-only. A member reading their own record uses /me/history, which
// takes the identity from the token instead of the URL.
func (h *MemberHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	member, err := h.members.Get(r.Context(), id)
	if err != nil {
		response.FromError(w, err)
		return
	}
	history, err := h.circulation.MemberHistory(r.Context(), id)
	if err != nil {
		response.FromError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"member":  toUserResponse(member),
		"history": toLoanResponses(history),
	}, nil)
}

type updateMemberStatusRequest struct {
	Status string `json:"status"`
}

// SetStatus suspends or reactivates a member (REQ-015).
func (h *MemberHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req updateMemberStatusRequest
	if !decode(w, r, &req) {
		return
	}

	status := domain.UserStatus(req.Status)
	switch status {
	case domain.UserActive, domain.UserSuspended, domain.UserInactive:
	default:
		response.ValidationError(w, "status must be active, suspended or inactive.", nil)
		return
	}

	if err := h.members.SetStatus(r.Context(), id, status); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": string(status)}, nil)
}
