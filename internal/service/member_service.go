package service

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

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
}

type MemberService struct {
	members  MemberStore
	notifier Notifier
}

func NewMemberService(m MemberStore, n Notifier) *MemberService {
	return &MemberService{members: m, notifier: n}
}

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
	IsSynthetic bool
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
// The temporary password is returned to the librarian to hand over, and the
// account is flagged to force a change at first login (REQ-007).
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

	// A random temporary password, never a predictable one derived from the
	// matriculation number: that pattern would let anyone guess any new account.
	temporary, _, err := auth.NewOpaqueToken()
	if err != nil {
		return domain.User{}, "", err
	}
	temporary = temporary[:12]

	hash, err := auth.HashPassword(temporary)
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

	_ = s.notifier.Queue(ctx, user.ID, "email", "welcome", map[string]any{
		"full_name":  user.FullName,
		"identifier": user.Identifier,
	})
	return user, temporary, nil
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
	Line       int    `json:"line"`
	Identifier string `json:"identifier"`
	Status     string `json:"status"` // valid | created | duplicate | invalid
	Detail     string `json:"detail,omitempty"`
	TempPass   string `json:"temporary_password,omitempty"`
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
	// Duplicates within the file itself are caught here; duplicates against the
	// database are caught by the unique constraint on insert.
	seen := make(map[string]int)
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
			Identifier: field("identifier"),
			Email:      field("email"),
			FullName:   field("full_name"),
			FirstName:  field("first_name"),
			LastName:   field("last_name"),
			Faculty:    field("faculty"),
			Department: field("department"),
			Level:      field("level"),
			Category:   domain.MemberCategory(strings.ToLower(field("category"))),
			Role:       domain.RoleMember,
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
			result.Valid++
			row.Status = "valid"
			result.Rows = append(result.Rows, row)
			continue
		}

		// Imported rows are always members. A CSV can never introduce staff.
		user, temporary, err := s.Create(ctx, actor, actorID, params)
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
			row.Status, row.TempPass = "created", temporary
			row.Identifier = user.Identifier
		}
		result.Rows = append(result.Rows, row)
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

// SetStatus suspends or reactivates a member (REQ-015).
func (s *MemberService) SetStatus(ctx context.Context, id uuid.UUID, status domain.UserStatus, staffID uuid.UUID) error {
	return s.members.UpdateStatus(ctx, id, status, staffID)
}
