package handler

import (
	"net/http"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

type AuthHandler struct{ auth *service.AuthService }

func NewAuthHandler(a *service.AuthService) *AuthHandler { return &AuthHandler{auth: a} }

type loginRequest struct {
	Login    string `json:"login"` // matriculation number, staff number or email
	Password string `json:"password"`
}

type userResponse struct {
	ID                 string  `json:"id"`
	Identifier         string  `json:"identifier"`
	Email              string  `json:"email"`
	FullName           string  `json:"full_name"`
	Role               string  `json:"role"`
	Category           *string `json:"category"`
	Status             string  `json:"status"`
	MustChangePassword bool    `json:"must_change_password"`
	// Visible so tooling, reports and the activity simulator can tell a
	// simulated borrower from a real student without guessing.
	IsSynthetic bool `json:"is_synthetic"`
}

func toUserResponse(u domain.User) userResponse {
	var category *string
	if u.Category != nil {
		c := string(*u.Category)
		category = &c
	}
	return userResponse{
		ID: u.ID.String(), Identifier: u.Identifier, Email: u.Email,
		FullName: u.FullName, Role: string(u.Role), Category: category,
		Status: string(u.Status), MustChangePassword: u.MustChangePassword,
		IsSynthetic: u.IsSynthetic,
	}
}

// Login exchanges credentials for a session (REQ-001).
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Login == "" || req.Password == "" {
		response.ValidationError(w, "Both login and password are required.", nil)
		return
	}

	session, err := h.auth.Login(r.Context(), req.Login, req.Password)
	if err != nil {
		response.FromError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"access_token":         session.AccessToken,
		"refresh_token":        session.RefreshToken,
		"expires_in":           session.ExpiresIn,
		"must_change_password": session.MustChangePassword,
		"user":                 toUserResponse(session.User),
	}, nil)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh rotates a session.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decode(w, r, &req) {
		return
	}
	session, err := h.auth.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"access_token":  session.AccessToken,
		"refresh_token": session.RefreshToken,
		"expires_in":    session.ExpiresIn,
	}, nil)
}

// Logout revokes the refresh token (REQ-006).
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.auth.Logout(r.Context(), req.RefreshToken); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "signed out"}, nil)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword updates the signed-in user's password (REQ-003, REQ-007).
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	var req changePasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.auth.ChangePassword(r.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "password changed"}, nil)
}

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPassword queues a reset link (REQ-004).
//
// The reply is identical whether or not the address is registered. Saying "no
// such account" would turn this endpoint into a way to test which students are
// members, and a borrowing history is not something to leak the existence of
// (DOM-009).
func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.auth.RequestPasswordReset(r.Context(), req.Email); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, map[string]string{
		"status": "If that address belongs to a member, a reset link is on its way.",
	}, nil)
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPassword completes a reset with a single-use token (REQ-005).
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.auth.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "password reset"}, nil)
}
