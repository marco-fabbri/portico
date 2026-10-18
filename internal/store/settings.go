package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GetSetting returns the raw value stored under key.
func (s *SQLite) GetSetting(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// SetSetting stores value under key, replacing any previous value.
func (s *SQLite) SetSetting(ctx context.Context, key string, value []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, formatTime(time.Now()))
	if err == nil {
		s.chmodFiles()
	}
	return err
}
