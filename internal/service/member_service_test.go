package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

type fakeMemberStore struct {
	created   []postgres.CreateUserParams
	conflicts map[string]bool
}

func (f *fakeMemberStore) Create(_ context.Context, p postgres.CreateUserParams) (domain.User, error) {
	if f.conflicts[p.Identifier] {
		return domain.User{}, domain.ErrConflict
	}
	f.created = append(f.created, p)
	return domain.User{ID: uuid.New(), Identifier: p.Identifier, FullName: p.FullName}, nil
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

	// Every created row is reported with the temporary password to hand over.
	for _, row := range result.Rows {
		if row.Status == "created" && row.TempPass == "" {
			t.Errorf("line %d was created without a temporary password to give the member", row.Line)
		}
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
func TestCreateIssuesUnpredictableTemporaryPassword(t *testing.T) {
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

	if first == second {
		t.Error("two members must not receive the same temporary password")
	}
	if strings.Contains(first, "SWE") || strings.Contains(first, "2025") {
		t.Error("the temporary password must not be derived from the matriculation number")
	}
	if len(first) < 10 {
		t.Errorf("temporary password is too short: %d characters", len(first))
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
