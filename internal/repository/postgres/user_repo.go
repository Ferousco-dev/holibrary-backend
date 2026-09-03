package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

type UserRepo struct{ db *pgxpool.Pool }

func NewUserRepo(db *pgxpool.Pool) *UserRepo { return &UserRepo{db: db} }

const userColumns = `id, identifier, email, full_name,
                     coalesce(first_name,''), coalesce(last_name,''),
                     coalesce(faculty,''), coalesce(department,''), coalesce(level,''),
                     role, category, status, must_change_password, created_at, updated_at`

func scanUser(row pgx.Row) (domain.User, error) {
	var u domain.User
	var category *string
	err := row.Scan(&u.ID, &u.Identifier, &u.Email, &u.FullName,
		&u.FirstName, &u.LastName, &u.Faculty, &u.Department, &u.Level,
		&u.Role, &category, &u.Status, &u.MustChangePassword,
		&u.CreatedAt, &u.UpdatedAt)
	if category != nil {
		c := domain.MemberCategory(*category)
		u.Category = &c
	}
	return u, err
}

// FindByLogin resolves an account by matriculation number, staff number or
// email, because members are told to sign in with whichever they remember
// (REQ-001). It also returns the password hash, which no other read does.
func (r *UserRepo) FindByLogin(ctx context.Context, login string) (domain.User, string, error) {
	const q = `SELECT ` + userColumns + `, password_hash
	             FROM users
	            WHERE lower(identifier) = lower($1) OR email = $1`

	var u domain.User
	var category *string
	var hash string
	err := r.db.QueryRow(ctx, q, strings.TrimSpace(login)).Scan(
		&u.ID, &u.Identifier, &u.Email, &u.FullName,
		&u.FirstName, &u.LastName, &u.Faculty, &u.Department, &u.Level,
		&u.Role, &category, &u.Status, &u.MustChangePassword,
		&u.CreatedAt, &u.UpdatedAt, &hash)
	if err != nil {
		return domain.User{}, "", translate(err)
	}
	if category != nil {
		c := domain.MemberCategory(*category)
		u.Category = &c
	}
	return u, hash, nil
}

func (r *UserRepo) FindByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1`
	u, err := scanUser(r.db.QueryRow(ctx, q, id))
	return u, translate(err)
}

func (r *UserRepo) FindByEmail(ctx context.Context, email string) (domain.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE email = $1`
	u, err := scanUser(r.db.QueryRow(ctx, q, email))
	return u, translate(err)
}

// CreateUserParams is what a librarian supplies when registering a member who
// has presented their identity card at the desk (DOM-006, REQ-009).
type CreateUserParams struct {
	Identifier   string
	Email        string
	FullName     string
	FirstName    string
	LastName     string
	Faculty      string
	Department   string
	Level        string
	PasswordHash string
	Role         domain.Role
	Category     *domain.MemberCategory
}

func (r *UserRepo) Create(ctx context.Context, p CreateUserParams) (domain.User, error) {
	const q = `INSERT INTO users (identifier, email, full_name, first_name, last_name,
	                             faculty, department, level, password_hash, role, category)
	           VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	           RETURNING ` + userColumns

	u, err := scanUser(r.db.QueryRow(ctx, q,
		p.Identifier, p.Email, p.FullName, nullif(p.FirstName), nullif(p.LastName),
		nullif(p.Faculty), nullif(p.Department), nullif(p.Level),
		p.PasswordHash, p.Role, p.Category))
	if err != nil {
		if isUniqueViolation(err, "") {
			return domain.User{}, domain.ErrConflict
		}
		return domain.User{}, translate(err)
	}
	return u, nil
}

// List returns members matching an optional search term, newest first.
func (r *UserRepo) List(ctx context.Context, search string, limit, offset int) ([]domain.User, int, error) {
	const q = `SELECT ` + userColumns + `, count(*) OVER() AS total
	             FROM users
	            WHERE ($1 = '' OR full_name ILIKE '%' || $1 || '%'
	                          OR identifier ILIKE '%' || $1 || '%'
	                          OR email ILIKE '%' || $1 || '%')
	            ORDER BY created_at DESC
	            LIMIT $2 OFFSET $3`

	rows, err := r.db.Query(ctx, q, search, limit, offset)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	var users []domain.User
	total := 0
	for rows.Next() {
		var u domain.User
		var category *string
		if err := rows.Scan(&u.ID, &u.Identifier, &u.Email, &u.FullName,
			&u.FirstName, &u.LastName, &u.Faculty, &u.Department, &u.Level,
			&u.Role, &category, &u.Status, &u.MustChangePassword,
			&u.CreatedAt, &u.UpdatedAt, &total); err != nil {
			return nil, 0, translate(err)
		}
		if category != nil {
			c := domain.MemberCategory(*category)
			u.Category = &c
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

func (r *UserRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.UserStatus) error {
	const q = `UPDATE users SET status = $2, updated_at = now() WHERE id = $1`
	tag, err := r.db.Exec(ctx, q, id, status)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdatePassword stores a new hash and clears the first-login flag, so the
// forced password change (REQ-007) resolves itself once the member complies.
func (r *UserRepo) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	const q = `UPDATE users
	              SET password_hash = $2, must_change_password = false, updated_at = now()
	            WHERE id = $1`
	tag, err := r.db.Exec(ctx, q, id, hash)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// PasswordHash reads only the hash, for verifying a current password.
func (r *UserRepo) PasswordHash(ctx context.Context, id uuid.UUID) (string, error) {
	var hash string
	err := r.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, id).Scan(&hash)
	return hash, translate(err)
}

// CountActiveLoans is used by the borrowing-limit check. It takes a pgx.Tx
// rather than the pool because the check and the loan insert must happen in one
// transaction, or a member could exceed their limit with concurrent requests
// (REQ-043).
func CountActiveLoans(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (int, error) {
	var n int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM loans WHERE user_id = $1 AND returned_at IS NULL`,
		userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting active loans: %w", err)
	}
	return n, nil
}
