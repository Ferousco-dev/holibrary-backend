package service

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

// MemberStore is the persistence the registration desk needs.
type MemberStore interface {
	Create(ctx context.Context, p postgres.CreateUserParams) (domain.User, error)
	List(ctx context.Context, search string, limit, offset int) ([]domain.User, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (domain.User, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.UserStatus, staffID uuid.UUID) error
	UpdateRole(ctx context.Context, id uuid.UUID, role domain.Role, category *domain.MemberCategory, staffID uuid.UUID) error
	ExistingIdentifiers(ctx context.Context, identifiers []string) (map[string]bool, error)
}

type MemberService struct {
	members  MemberStore
	notifier Notifier
	tokens   TokenStore
}

type trackedNotifier interface {
	QueueTracked(ctx context.Context, userID uuid.UUID, channel, template string, payload map[string]any) (uuid.UUID, error)
}

type invitationInvalidator interface {
	InvalidateInvitations(ctx context.Context, memberID uuid.UUID) error
}

func NewMemberService(m MemberStore, n Notifier, tokens ...TokenStore) *MemberService {
	var tokenStore TokenStore
	if len(tokens) > 0 {
		tokenStore = tokens[0]
	}
	return &MemberService{members: m, notifier: n, tokens: tokenStore}
}

// AccountSetupTTL gives a new member enough time to act without weakening the
// shorter forgotten-password reset window.
const AccountSetupTTL = 7 * 24 * time.Hour

// NewMemberParams is what the librarian types in after the applicant has
// presented their identity card at the desk (DOM-006).
type NewMemberParams struct {
	Identifier string
	Email      string
	FullName   string
	FirstName  string
	LastName   string
	Faculty    string
	Department string
	Level      string
	Category   domain.MemberCategory
	Role       domain.Role
	// IsSynthetic marks a simulated borrower (DEC-021).
	IsSynthetic       bool
	InvitationBatchID uuid.UUID
}

// displayName assembles the name shown across the system.
//
// The librarian may type a single full name, or first and last separately from
// the identity card. Either is accepted, so the CSV a department secretary
// exports does not have to be reshaped before import.
func (p NewMemberParams) displayName() string {
	if full := strings.TrimSpace(p.FullName); full != "" {
		return full
	}
	return strings.TrimSpace(strings.TrimSpace(p.FirstName) + " " + strings.TrimSpace(p.LastName))
}

// RolesCreatableBy reports which roles an actor may create.
//
// A librarian registers members and nothing else. Without this, the role field
// on the create-member request was a privilege-escalation vector: any librarian
// could post {"role":"admin"} and mint themselves an administrator. Staff
// accounts are created by administrators only. DEF-005.
func RolesCreatableBy(actor domain.Role) map[domain.Role]bool {
	switch actor {
	case domain.RoleAdmin:
		return map[domain.Role]bool{
			domain.RoleMember: true, domain.RoleLibrarian: true, domain.RoleAdmin: true,
		}
	case domain.RoleLibrarian:
		return map[domain.Role]bool{domain.RoleMember: true}
	default:
		return map[domain.Role]bool{}
	}
}

// Create registers a member.
//
// There is no self-registration anywhere in this system. Membership begins in
// the building, which is both how HOL works and the reason an attacker cannot
// mint themselves an account (DEC-006, REQ-002, REQ-009).
//
// New accounts receive a one-time setup link. The password hash is random and
// never leaves this method; the account remains restricted until setup.
func (s *MemberService) Create(ctx context.Context, actor domain.Role, actorID uuid.UUID, p NewMemberParams) (domain.User, string, error) {
	if err := validateNewMember(p); err != nil {
		return domain.User{}, "", err
	}

	// The role is taken from the request body, so it is checked against what
	// the caller is actually allowed to create (DEF-005).
	requested := p.Role
	if requested == "" {
		requested = domain.RoleMember
	}
	if !RolesCreatableBy(actor)[requested] {
		return domain.User{}, "", domain.ErrForbidden
	}

	// The account starts with a random unusable password. It is never returned
	// or sent by email; the member sets the real password through the invitation.
	bootstrap, _, err := auth.NewOpaqueToken()
	if err != nil {
		return domain.User{}, "", err
	}

	hash, err := auth.HashPassword(bootstrap)
	if err != nil {
		return domain.User{}, "", err
	}

	role := requested

	var category *domain.MemberCategory
	if role == domain.RoleMember {
		c := p.Category
		category = &c
	}

	user, err := s.members.Create(ctx, postgres.CreateUserParams{
		CreatedBy:    actorID,
		Identifier:   strings.TrimSpace(p.Identifier),
		Email:        strings.ToLower(strings.TrimSpace(p.Email)),
		FullName:     p.displayName(),
		FirstName:    strings.TrimSpace(p.FirstName),
		LastName:     strings.TrimSpace(p.LastName),
		Faculty:      strings.TrimSpace(p.Faculty),
		Department:   strings.TrimSpace(p.Department),
		Level:        strings.TrimSpace(p.Level),
		PasswordHash: hash,
		Role:         role,
		Category:     category,
		IsSynthetic:  p.IsSynthetic,
	})
	if err != nil {
		return domain.User{}, "", err
	}

	setupToken, setupHash, err := auth.NewOpaqueToken()
	if err != nil {
		return domain.User{}, "", err
	}
	if s.tokens != nil {
		if err := s.tokens.SavePasswordReset(ctx, user.ID, setupHash, time.Now().UTC().Add(AccountSetupTTL)); err != nil {
			return domain.User{}, "", err
		}
	}
	if s.notifier != nil {
		payload := map[string]any{
			"full_name": user.FullName,
			"token":     setupToken,
		}
		if p.InvitationBatchID != uuid.Nil {
			payload["batch_id"] = p.InvitationBatchID.String()
		}
		if tracked, ok := s.notifier.(trackedNotifier); ok {
			if _, err := tracked.QueueTracked(ctx, user.ID, "email", "account_setup", payload); err != nil {
				return domain.User{}, "", err
			}
		} else if err := s.notifier.Queue(ctx, user.ID, "email", "account_setup", payload); err != nil {
			return domain.User{}, "", err
		}
	}
	return user, "", nil
}

func validateNewMember(p NewMemberParams) error {
	switch {
	case strings.TrimSpace(p.Identifier) == "":
		return fmt.Errorf("matriculation or staff number is required")
	case !strings.Contains(p.Email, "@"):
		return fmt.Errorf("a valid email address is required")
	case p.displayName() == "":
		return fmt.Errorf("full name, or first and last name, is required")
	}

	// A member without a category has no borrowing entitlement, so the category
	// is required rather than defaulted: guessing it would quietly grant the
	// wrong loan terms (DOM-005).
	if p.Role == "" || p.Role == domain.RoleMember {
		if _, ok := domain.TermsFor(p.Category); !ok {
			return fmt.Errorf("category must be undergraduate, postgraduate or staff")
		}
	}
	return nil
}

// ImportRow is the outcome of one line of a CSV import.
type ImportRow struct {
	Line                  int    `json:"line"`
	Identifier            string `json:"identifier"`
	Status                string `json:"status"` // valid | created | duplicate | invalid
	Detail                string `json:"detail,omitempty"`
	InvitationEmailQueued bool   `json:"invitation_email_queued,omitempty"`
}

// ImportResult summarises a whole file.
//
// The counts are what a librarian actually wants to see after uploading eight
// hundred rows: how many went in, how many were already registered, and exactly
// which lines need fixing.
type ImportResult struct {
	DryRun    bool        `json:"dry_run"`
	TotalRows int         `json:"total_rows"`
	Valid     int         `json:"valid"`
	Created   int         `json:"created"`
	Duplicate int         `json:"duplicate"`
	Invalid   int         `json:"invalid"`
	Rows      []ImportRow `json:"rows"`
}

// csvAliases maps the column names a real spreadsheet is likely to carry onto
// the fields this service needs. A departmental export says "student_id"; an
// earlier version of this API said "identifier". Both are accepted, because
// telling a librarian to rename columns before importing is a good way to have
// the feature go unused.
var csvAliases = map[string][]string{
	"identifier": {"identifier", "student_id", "matric_no", "matric_number"},
	"email":      {"email", "e-mail", "email_address"},
	"first_name": {"first_name", "firstname", "given_name"},
	"last_name":  {"last_name", "lastname", "surname", "family_name"},
	"full_name":  {"full_name", "fullname", "name"},
	"department": {"department", "dept"},
	"faculty":    {"faculty"},
	"level":      {"level", "year"},
	"category":   {"category", "member_category"},
}

// ImportCSV registers many members from a spreadsheet export (REQ-010).
//
// Expected header, in any order, with common aliases accepted:
//
//	student_id,first_name,last_name,email,department,level
//
// When dryRun is true nothing is written. The file is parsed and validated and
// the same summary is returned, so a librarian can preview eight hundred rows,
// fix the seven bad addresses, and only then commit. Importing blind and
// discovering the damage afterwards is how a member roll gets corrupted.
//
// A bad row never aborts the batch. It is counted, named by line number, and
// skipped (REQ-011).
func (s *MemberService) ImportCSV(ctx context.Context, actor domain.Role, actorID uuid.UUID, r io.Reader, dryRun bool) (ImportResult, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // rows are validated individually below

	header, err := reader.Read()
	if err != nil {
		return ImportResult{}, fmt.Errorf("reading CSV header: %w", err)
	}
	index, err := columnIndex(header)
	if err != nil {
		return ImportResult{}, err
	}

	result := ImportResult{DryRun: dryRun}
	batchID := uuid.New()
	// Duplicates within the file itself are caught here. Duplicates against the
	// database are caught by the unique constraint on insert during a real run,
	// but a dry run never inserts, so it has to ask. pending records where each
	// dry-run row landed in result.Rows, so one query at the end can settle them
	// all rather than one query per row.
	seen := make(map[string]int)
	pending := map[int]string{}
	line := 1

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		result.TotalRows++

		if err != nil {
			result.Invalid++
			result.Rows = append(result.Rows, ImportRow{
				Line: line, Status: "invalid", Detail: "malformed row: " + err.Error(),
			})
			continue
		}

		field := func(name string) string {
			i, ok := index[name]
			if !ok || i >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[i])
		}

		params := NewMemberParams{
			Identifier:        field("identifier"),
			Email:             field("email"),
			FullName:          field("full_name"),
			FirstName:         field("first_name"),
			LastName:          field("last_name"),
			Faculty:           field("faculty"),
			Department:        field("department"),
			Level:             field("level"),
			Category:          domain.MemberCategory(strings.ToLower(field("category"))),
			Role:              domain.RoleMember,
			InvitationBatchID: batchID,
		}
		// Most rows in a student intake are undergraduates and the column is
		// often absent, so an empty category defaults rather than failing.
		if params.Category == "" {
			params.Category = domain.CategoryUndergraduate
		}

		row := ImportRow{Line: line, Identifier: params.Identifier}

		// Shape errors are reported without touching the database at all.
		if err := validateNewMember(params); err != nil {
			result.Invalid++
			row.Status, row.Detail = "invalid", err.Error()
			result.Rows = append(result.Rows, row)
			continue
		}

		if first, repeated := seen[strings.ToLower(params.Identifier)]; repeated {
			result.Duplicate++
			row.Status = "duplicate"
			row.Detail = fmt.Sprintf("same identifier as line %d of this file", first)
			result.Rows = append(result.Rows, row)
			continue
		}
		seen[strings.ToLower(params.Identifier)] = line

		if dryRun {
			pending[len(result.Rows)] = params.Identifier
			result.Rows = append(result.Rows, row)
			continue
		}

		// Imported rows are always members. A CSV can never introduce staff.
		user, _, err := s.Create(ctx, actor, actorID, params)
		switch {
		case errors.Is(err, domain.ErrConflict):
			// Re-importing last session's roll is routine, not a failure.
			result.Duplicate++
			row.Status, row.Detail = "duplicate", "already registered"
		case err != nil:
			result.Invalid++
			row.Status, row.Detail = "invalid", err.Error()
		default:
			result.Created++
			result.Valid++
			row.Status = "created"
			row.InvitationEmailQueued = s.notifier != nil
			row.Identifier = user.Identifier
		}
		result.Rows = append(result.Rows, row)
	}

	// A dry run has to ask the database what it would have collided with,
	// because it never inserts and so never trips the unique constraint that
	// catches this on a real run. Without it a librarian previewing last
	// term's roll is told eight hundred accounts are ready, and creates four.
	if dryRun && len(pending) > 0 {
		wanted := make([]string, 0, len(pending))
		for _, identifier := range pending {
			wanted = append(wanted, identifier)
		}

		taken, err := s.members.ExistingIdentifiers(ctx, wanted)
		if err != nil {
			return ImportResult{}, err
		}

		for at, identifier := range pending {
			if taken[strings.ToLower(strings.TrimSpace(identifier))] {
				result.Duplicate++
				result.Rows[at].Status = "duplicate"
				result.Rows[at].Detail = "already registered"
				continue
			}
			result.Valid++
			result.Rows[at].Status = "valid"
		}
	}

	return result, nil
}

// columnIndex resolves the header, accepting the aliases in csvAliases.
func columnIndex(header []string) (map[string]int, error) {
	present := make(map[string]int, len(header))
	for i, h := range header {
		present[strings.ToLower(strings.TrimSpace(h))] = i
	}

	index := make(map[string]int)
	for field, aliases := range csvAliases {
		for _, alias := range aliases {
			if i, ok := present[alias]; ok {
				index[field] = i
				break
			}
		}
	}

	if _, ok := index["identifier"]; !ok {
		return nil, fmt.Errorf("CSV needs a student_id (or identifier) column; header was: %s",
			strings.Join(header, ","))
	}
	if _, ok := index["email"]; !ok {
		return nil, fmt.Errorf("CSV needs an email column; header was: %s",
			strings.Join(header, ","))
	}
	_, hasFull := index["full_name"]
	_, hasFirst := index["first_name"]
	if !hasFull && !hasFirst {
		return nil, fmt.Errorf("CSV needs either a full_name column or first_name and last_name columns")
	}
	return index, nil
}

func (s *MemberService) List(ctx context.Context, search string, limit, offset int) ([]domain.User, int, error) {
	return s.members.List(ctx, search, limit, offset)
}

func (s *MemberService) Get(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return s.members.FindByID(ctx, id)
}

// ResendInvitation invalidates every previous setup link before queueing a new
// one. The token itself is kept only in the queued email payload.
func (s *MemberService) ResendInvitation(ctx context.Context, id uuid.UUID) error {
	user, err := s.members.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if s.tokens == nil || s.notifier == nil {
		return fmt.Errorf("invitation delivery is not configured")
	}
	if err := s.tokens.InvalidatePasswordResets(ctx, user.ID); err != nil {
		return err
	}
	if invalidator, ok := s.notifier.(invitationInvalidator); ok {
		if err := invalidator.InvalidateInvitations(ctx, user.ID); err != nil {
			return err
		}
	}
	token, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	if err := s.tokens.SavePasswordReset(ctx, user.ID, hash, time.Now().UTC().Add(AccountSetupTTL)); err != nil {
		return err
	}
	payload := map[string]any{"full_name": user.FullName, "token": token}
	if tracked, ok := s.notifier.(trackedNotifier); ok {
		_, err = tracked.QueueTracked(ctx, user.ID, "email", "account_setup", payload)
	} else {
		err = s.notifier.Queue(ctx, user.ID, "email", "account_setup", payload)
	}
	return err
}

// SetStatus suspends or reactivates a member (REQ-015).
func (s *MemberService) SetStatus(ctx context.Context, id uuid.UUID, status domain.UserStatus, staffID uuid.UUID) error {
	return s.members.UpdateStatus(ctx, id, status, staffID)
}

// SetRole changes an account's authorization role. The HTTP route is admin-only;
// repository enforcement protects the last-administrator invariant atomically.
func (s *MemberService) SetRole(ctx context.Context, id uuid.UUID, role domain.Role, category *domain.MemberCategory, staffID uuid.UUID) error {
	if category != nil {
		if _, valid := domain.TermsFor(*category); !valid || role != domain.RoleMember {
			return domain.ErrInvalidMemberCategory
		}
	}
	switch role {
	case domain.RoleMember, domain.RoleLibrarian, domain.RoleAdmin:
		return s.members.UpdateRole(ctx, id, role, category, staffID)
	default:
		return fmt.Errorf("role must be member, librarian or admin")
	}
}
