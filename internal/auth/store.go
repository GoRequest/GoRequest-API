package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

const sessionLifetime = 7 * 24 * time.Hour

type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys = ON;
	PRAGMA busy_timeout = 5000;
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS sessions (
		token_hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

var errEmailExists = errors.New("email already registered")

func (s *Store) createUser(ctx context.Context, email, hash string) (User, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO users(email, password_hash) VALUES (?, ?) ON CONFLICT(email) DO NOTHING`, email, hash)
	if err != nil {
		return User{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if n == 0 {
		return User{}, errEmailExists
	}
	id, err := result.LastInsertId()
	return User{ID: id, Email: email}, err
}

func (s *Store) credentials(ctx context.Context, email string) (User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, email, password_hash FROM users WHERE email = ?`, email).Scan(&u.ID, &u.Email, &hash)
	return u, hash, err
}

func (s *Store) newSession(ctx context.Context, userID int64, previous string) (string, time.Time, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(token))
	expires := time.Now().Add(sessionLifetime).Truncate(time.Second)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		return "", time.Time{}, err
	}
	if previous != "" {
		oldHash := sha256.Sum256([]byte(previous))
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, oldHash[:]); err != nil {
			return "", time.Time{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, expires_at) VALUES (?, ?, ?)`, hash[:], userID, expires.Unix()); err != nil {
		return "", time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (s *Store) sessionUser(ctx context.Context, token string) (User, error) {
	var u User
	if len(token) != 43 {
		return u, sql.ErrNoRows
	}
	hash := sha256.Sum256([]byte(token))
	err := s.db.QueryRowContext(ctx, `SELECT users.id, users.email FROM sessions JOIN users ON users.id = sessions.user_id WHERE token_hash = ? AND expires_at > ?`, hash[:], time.Now().Unix()).Scan(&u.ID, &u.Email)
	return u, err
}

func (s *Store) revoke(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash[:])
	return err
}
