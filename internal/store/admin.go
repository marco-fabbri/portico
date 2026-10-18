package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SetAdminPassword stores the bcrypt hash of a break-glass account,
// replacing any previous one. The hash is the only thing ever stored.
func (s *SQLite) SetAdminPassword(ctx context.Context, name, bcryptHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_accounts (name, password_hash, updated_at) VALUES (?,?,?)
		ON CONFLICT(name) DO UPDATE SET password_hash = excluded.password_hash, updated_at = excluded.updated_at`,
		name, bcryptHash, formatTime(time.Now()))
	if err == nil {
		s.chmodFiles()
	}
	return err
}

// AdminPasswordHash returns the stored hash of an account.
func (s *SQLite) AdminPasswordHash(ctx context.Context, name string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin_accounts WHERE name = ?`, name).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}
