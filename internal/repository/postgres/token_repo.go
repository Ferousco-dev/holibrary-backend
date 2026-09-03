package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// TokenRepo stores the revocable half of a session and one-shot reset tokens.
//
// Only hashes are stored. A leaked database therefore yields no usable session
// and no usable reset link, which is the difference between an embarrassing
// incident and a total compromise (NFR-003).
type TokenRepo struct{ db *pgxpool.Pool }

func NewTokenRepo(db *pgxpool.Pool) *TokenRepo { return &TokenRepo{db: db} }

func (r *TokenRepo) SaveRefreshToken(ctx context.Context, userID uuid.UUID, hash string, expires time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1,$2,$3)`,
		userID, hash, expires)
	return translate(err)
}

// ConsumeRefreshToken revokes the token and returns its owner.
//
// Revoking as part of the lookup makes refresh single-use: the token is rotated
// on every exchange, so a stolen one is good for at most one call.
func (r *TokenRepo) ConsumeRefreshToken(ctx context.Context, hash string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := r.db.QueryRow(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING user_id`, hash).Scan(&userID)
	if err != nil {
		return uuid.Nil, translate(err)
	}
	return userID, nil
}

func (r *TokenRepo) RevokeRefreshToken(ctx context.Context, hash string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		  WHERE token_hash = $1 AND revoked_at IS NULL`, hash)
	return translate(err)
}

// RevokeAllRefreshTokens ends every session for a user.
//
// Called on password change and on password reset, so a credential that may
// have been compromised cannot keep a session alive behind the change (DEF-006).
func (r *TokenRepo) RevokeAllRefreshTokens(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		  WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return translate(err)
}

func (r *TokenRepo) SavePasswordReset(ctx context.Context, userID uuid.UUID, hash string, expires time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO password_resets (user_id, token_hash, expires_at) VALUES ($1,$2,$3)`,
		userID, hash, expires)
	return translate(err)
}

// ConsumePasswordReset marks the token used and returns its owner. Marking and
// reading in one statement is what makes it single-use under concurrency
// (REQ-005).
func (r *TokenRepo) ConsumePasswordReset(ctx context.Context, hash string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := r.db.QueryRow(ctx, `
		UPDATE password_resets SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`, hash).Scan(&userID)
	if err != nil {
		return uuid.Nil, domain.ErrTokenInvalid
	}
	return userID, nil
}
