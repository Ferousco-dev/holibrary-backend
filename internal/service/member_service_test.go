package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

type fakeMemberStore struct {
	created   []postgres.CreateUserParams
	conflicts map[string]bool
	role      domain.Role
	category  *domain.MemberCategory
}

func (f *fakeMemberStore) Create(_ context.Context, p postgres.CreateUserParams) (domain.User, error) {
	if f.conflicts[p.Identifier] {
		return domain.User{}, domain.ErrConflict
	}
	f.created = append(f.created, p)
	return domain.User{ID: uuid.New(), Identifier: p.Identifier, FullName: p.FullName}, nil
}

// The fake's conflicts map stands in for the roll a dry run would collide
// with, so a preview in a test sees the same duplicates a real one would.
func (f *fakeMemberStore) ExistingIdentifiers(_ context.Context, ids []string) (map[string]bool, error) {
	found := map[string]bool{}
	for _, id := range ids {
		for conflicting := range f.conflicts {
			if strings.EqualFold(conflicting, id) {
				found[strings.ToLower(strings.TrimSpace(id))] = true
			}
		}
	}
	return found, nil
}

func (f *fakeMemberStore) List(context.Context, string, int, int) ([]domain.User, int, error) {
	return nil, 0, nil
}
func (f *fakeMemberStore) FindByID(context.Context, uuid.UUID) (domain.User, error) {
	return domain.User{}, nil
}
func (f *fakeMemberStore) UpdateStatus(context.Context, uuid.UUID, domain.UserStatus, uuid.UUID) error {
	return nil
}

func (f *fakeMemberStore) UpdateRole(_ context.Context, _ uuid.UUID, role domain.Role, category *domain.MemberCategory, _ uuid.UUID) error {
	f.role = role
	f.category = category
	return nil
}

// The header a departmental secretary actually exports, not one this API
// invented. student_id and matric_no must both resolve to the identifier.
const rollCSV = `student_id,first_name,last_name,email,department,level
SWE/2025/001,Feranmi,Oresajo,feranmi@oauife.edu.ng,Software Engineering,200
SWE/2025/002,John,Doe,john@oauife.edu.ng,Software Engineering,200
SWE/2025/003,Bad,Email,not-an-email,Software Engineering,200
SWE/2025/002,Repeat,Row,repeat@oauife.edu.ng,Software Engineering,200
,Missing,Id,missing@oauife.edu.ng,Software Engineering,200
`

// A dry run reports exactly what a commit would do, and writes nothing. This is
// what lets a librarian preview eight hundred rows before touching the roll.
func TestImportCSVDryRunWritesNothing(t *testing.T) {
	store := &fakeMemberStore{}
	svc := service.NewMemberService(store, &fakeNotifier{})

	result, err := svc.ImportCSV(context.Background(), domain.RoleLibrarian, uuid.Nil, strings.NewReader(rollCSV), true)
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}

	if !result.DryRun {
		t.Error("the result must say it was a dry run")
	}
	if len(store.created) != 0 {
		t.Errorf("a dry run must not create anyone, created %d", len(store.created))
	}
	if result.TotalRows != 5 {
		t.Errorf("TotalRows = %d, want 5", result.TotalRows)
	}
	if result.Valid != 2 {
		t.Errorf("Valid = %d, want 2", result.Valid)
	}
	if result.Duplicate != 1 {
		t.Errorf("Duplicate = %d, want 1 (SWE/2025/002 repeated in-file)", result.Duplicate)
	}
	if result.Invalid != 2 {
		t.Errorf("Invalid = %d, want 2 (bad email, missing id)", result.Invalid)
	}
}

// One bad address must not cost the librarian the other rows (REQ-011).
func TestImportCSVCommitsGoodRowsDespiteBadOnes(t *testing.T) {
	store := &fakeMemberStore{}
	svc := service.NewMemberService(store, &fakeNotifier{})

	result, err := svc.ImportCSV(context.Background(), domain.RoleLibrarian, uuid.Nil, strings.NewReader(rollCSV), false)
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}

	if result.Created != 2 {
		t.Errorf("Created = %d, want 2", result.Created)
	}
	if len(store.created) != 2 {
		t.Fatalf("store received %d members, want 2", len(store.created))
	}

	// first_name and last_name are combined into the display name.
	if got := store.created[0].FullName; got != "Feranmi Oresajo" {
		t.Errorf("FullName = %q, want %q", got, "Feranmi Oresajo")
	}
	if got := store.created[0].Department; got != "Software Engineering" {
		t.Errorf("Department = %q, want %q", got, "Software Engineering")
	}
	if got := store.created[0].Level; got != "200" {
		t.Errorf("Level = %q, want 200", got)
	}

	// Created rows report only whether the invitation was queued.
	for _, row := range result.Rows {
		if row.Status == "created" && !row.InvitationEmailQueued {
			t.Errorf("line %d was created without a queued invitation", row.Line)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "temporary_password") {
		t.Error("CSV results must not contain temporary passwords")
	}
}

// Re-importing last session's roll is routine, and must be reported as a
// duplicate rather than an error.
func TestImportCSVReportsAlreadyRegistered(t *testing.T) {
	store := &fakeMemberStore{conflicts: map[string]bool{"SWE/2025/001": true}}
	svc := service.NewMemberService(store, &fakeNotifier{})

	result, err := svc.ImportCSV(context.Background(), domain.RoleLibrarian, uuid.Nil, strings.NewReader(rollCSV), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate != 2 {
		t.Errorf("Duplicate = %d, want 2 (one in-file repeat, one already registered)", result.Duplicate)
	}
	if result.Created != 1 {
		t.Errorf("Created = %d, want 1", result.Created)
	}
}

// A header this system cannot understand is a whole-file error, reported before
// anything is written.
func TestImportCSVRejectsUnusableHeader(t *testing.T) {
	svc := service.NewMemberService(&fakeMemberStore{}, &fakeNotifier{})

	_, err := svc.ImportCSV(context.Background(), domain.RoleLibrarian, uuid.Nil, strings.NewReader("name,phone\nSomeone,08000000000\n"), true)
	if err == nil {
		t.Fatal("a header with no student id must be rejected")
	}
	if !strings.Contains(err.Error(), "student_id") {
		t.Errorf("the error should name the missing column, got %q", err)
	}
}

// A new member never receives a password derived from their matriculation
// number: that pattern would let anyone sign in as any new student.
func TestCreateDoesNotReturnAPassword(t *testing.T) {
	store := &fakeMemberStore{}
	svc := service.NewMemberService(store, &fakeNotifier{})

	params := service.NewMemberParams{
		Identifier: "SWE/2025/010", Email: "a@oauife.edu.ng",
		FirstName: "Ada", LastName: "Obi", Category: domain.CategoryUndergraduate,
	}

	_, first, err := svc.Create(context.Background(), domain.RoleLibrarian, uuid.Nil, params)
	if err != nil {
		t.Fatal(err)
	}
	params.Identifier = "SWE/2025/011"
	_, second, err := svc.Create(context.Background(), domain.RoleLibrarian, uuid.Nil, params)
	if err != nil {
		t.Fatal(err)
	}

	if first != "" || second != "" {
		t.Error("member creation must not return readable passwords")
	}
}

func TestCreateStoresHashedSevenDayInvitationAndQueuesOneEmail(t *testing.T) {
	store := &fakeMemberStore{}
	notifier := &fakeNotifier{}
	tokens := &fakeTokens{}
	svc := service.NewMemberService(store, notifier, tokens)

	user, returned, err := svc.Create(context.Background(), domain.RoleLibrarian, uuid.Nil, service.NewMemberParams{
		Identifier: "SWE/2025/020", Email: "invite@oauife.edu.ng",
		FirstName: "Invite", LastName: "Student", Category: domain.CategoryUndergraduate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if returned != "" {
		t.Fatal("creation must not return a readable password")
	}
	if len(notifier.queued) != 1 || notifier.queued[0] != "account_setup" {
		t.Fatalf("queued notifications = %v, want one account_setup", notifier.queued)
	}
	raw, ok := notifier.payloads[0]["token"].(string)
	if !ok || raw == "" {
		t.Fatal("invitation email must receive a raw token for its link")
	}
	if tokens.savedReset != auth.HashToken(raw) || tokens.savedReset == raw {
		t.Fatal("only the invitation token hash may be stored")
	}
	if tokens.resetOwner != user.ID {
		t.Fatal("invitation token belongs to the created user")
	}
	remaining := time.Until(tokens.resetExpires)
	if remaining < 6*24*time.Hour || remaining > 7*24*time.Hour {
		t.Fatalf("invitation expiry = %s, want about seven days", remaining)
	}
	if _, err := tokens.ConsumePasswordReset(context.Background(), tokens.savedReset); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.ConsumePasswordReset(context.Background(), tokens.savedReset); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Fatalf("second invitation use = %v, want ErrTokenInvalid", err)
	}
}

// A librarian who posts {"role":"admin"} must be refused. Without this check the
// role field on the create-member request is a privilege-escalation vector:
// any librarian could mint themselves an administrator (DEF-005).
func TestLibrarianCannotCreateStaffAccounts(t *testing.T) {
	store := &fakeMemberStore{}
	svc := service.NewMemberService(store, &fakeNotifier{})

	for _, role := range []domain.Role{domain.RoleAdmin, domain.RoleLibrarian} {
		_, _, err := svc.Create(context.Background(), domain.RoleLibrarian, uuid.Nil, service.NewMemberParams{
			Identifier: "SWE/2025/999", Email: "x@oauife.edu.ng",
			FirstName: "Priv", LastName: "Escalation",
			Category: domain.CategoryUndergraduate, Role: role,
		})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("librarian creating %s: error = %v, want ErrForbidden", role, err)
		}
	}
	if len(store.created) != 0 {
		t.Error("no account should have been created")
	}

	// An administrator may.
	if _, _, err := svc.Create(context.Background(), domain.RoleAdmin, uuid.Nil, service.NewMemberParams{
		Identifier: "LIB/STAFF/002", Email: "staff@oauife.edu.ng",
		FirstName: "New", LastName: "Librarian", Role: domain.RoleLibrarian,
	}); err != nil {
		t.Errorf("an administrator may create a librarian: %v", err)
	}
}

func TestSetRoleAcceptsSupportedRoles(t *testing.T) {
	store := &fakeMemberStore{}
	svc := service.NewMemberService(store, nil)
	for _, role := range []domain.Role{domain.RoleMember, domain.RoleLibrarian, domain.RoleAdmin} {
		if err := svc.SetRole(context.Background(), uuid.New(), role, nil, uuid.New()); err != nil {
			t.Fatalf("SetRole(%s): %v", role, err)
		}
		if store.role != role {
			t.Fatalf("stored role = %s, want %s", store.role, role)
		}
	}
}

func TestSetRoleRejectsUnsupportedRoles(t *testing.T) {
	store := &fakeMemberStore{}
	svc := service.NewMemberService(store, nil)
	if err := svc.SetRole(context.Background(), uuid.New(), domain.Role("owner"), nil, uuid.New()); err == nil {
		t.Fatal("unsupported role must be rejected")
	}
	if store.role != "" {
		t.Fatal("unsupported role must not reach the store")
	}
}

// A preview that cannot see the existing roll is worse than no preview: it
// tells a librarian eight hundred accounts are ready and then creates four.
func TestDryRunCountsMembersAlreadyOnTheRoll(t *testing.T) {
	store := &fakeMemberStore{conflicts: map[string]bool{"SEN/2025/101": true}}
	svc := service.NewMemberService(store, nil)

	const roll = "student_id,first_name,last_name,email\n" +
		"SEN/2025/101,Amaka,Nwosu,amaka@oauife.edu.ng\n" +
		"SEN/2025/102,Tunde,Bakare,tunde@oauife.edu.ng\n"

	result, err := svc.ImportCSV(context.Background(), domain.RoleLibrarian, uuid.Nil,
		strings.NewReader(roll), true)
	if err != nil {
		t.Fatal(err)
	}

	if result.Duplicate != 1 {
		t.Errorf("duplicate = %d, want 1: the first row is already registered", result.Duplicate)
	}
	if result.Valid != 1 {
		t.Errorf("valid = %d, want 1: only the second row is new", result.Valid)
	}
	if len(store.created) != 0 {
		t.Errorf("a dry run wrote %d members; it must write none", len(store.created))
	}
}

func TestSetRoleValidatesBorrowingCategory(t *testing.T) {
	for _, category := range []domain.MemberCategory{domain.CategoryUndergraduate, domain.CategoryPostgraduate, domain.CategoryStaff} {
		store := &fakeMemberStore{}
		svc := service.NewMemberService(store, nil)
		if err := svc.SetRole(context.Background(), uuid.New(), domain.RoleMember, &category, uuid.New()); err != nil {
			t.Fatal(err)
		}
		if store.category == nil || *store.category != category {
			t.Fatal("category was not passed to repository")
		}
	}
	// Invalid category strings are refused regardless of role. A category is
	// permitted for any role (staff and admins may borrow), so librarian +
	// staff-category and admin + undergraduate-category are valid and are
	// exercised elsewhere in this file.
	for _, test := range []struct {
		role     domain.Role
		category domain.MemberCategory
	}{{domain.RoleMember, ""}, {domain.RoleMember, "student"}, {domain.RoleLibrarian, "student"}, {domain.RoleAdmin, ""}} {
		store := &fakeMemberStore{}
		svc := service.NewMemberService(store, nil)
		if err := svc.SetRole(context.Background(), uuid.New(), test.role, &test.category, uuid.New()); !errors.Is(err, domain.ErrInvalidMemberCategory) {
			t.Fatalf("invalid category: %v", err)
		}
		if store.role != "" {
			t.Fatal("invalid request reached repository")
		}
	}

	// Staff and admin accounts may now hold a borrowing category, so that the
	// same person can both manage the library and borrow from it.
	for _, test := range []struct {
		role     domain.Role
		category domain.MemberCategory
	}{{domain.RoleLibrarian, domain.CategoryStaff}, {domain.RoleAdmin, domain.CategoryUndergraduate}} {
		store := &fakeMemberStore{}
		svc := service.NewMemberService(store, nil)
		if err := svc.SetRole(context.Background(), uuid.New(), test.role, &test.category, uuid.New()); err != nil {
			t.Fatalf("staff/admin with a category should be accepted: %v", err)
		}
		if store.role != test.role || store.category == nil || *store.category != test.category {
			t.Fatalf("role/category not passed to repository (%v, %v)", store.role, store.category)
		}
	}
}
