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
		id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE
	);
	CREATE TABLE IF NOT EXISTS sessions (
		token_hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
	CREATE TABLE IF NOT EXISTS github_identities (
		github_id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS oauth_flows (
		state_hash BLOB PRIMARY KEY, verifier TEXT NOT NULL, previous_hash BLOB,
		expires_at INTEGER NOT NULL
	);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Preserve legacy accounts but remove password hashes and revoke pre-OAuth sessions.
	var legacyPasswordColumn int
	err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('users') WHERE name = 'password_hash'`).Scan(&legacyPasswordColumn)
	if err == nil && legacyPasswordColumn != 0 {
		var tx *sql.Tx
		tx, err = db.Begin()
		if err == nil {
			_, err = tx.Exec(`ALTER TABLE users DROP COLUMN password_hash; DELETE FROM sessions;`)
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

var errEmailExists = errors.New("email already registered")

func (s *Store) newSession(ctx context.Context, userID int64, previous string) (string, time.Time, error) {
	var previousHash []byte
	if previous != "" {
		hash := sha256.Sum256([]byte(previous))
		previousHash = hash[:]
	}
	return s.newSessionWithHash(ctx, userID, previousHash)
}

func (s *Store) newSessionWithHash(ctx context.Context, userID int64, previousHash []byte) (string, time.Time, error) {
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
	if len(previousHash) != 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, previousHash); err != nil {
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
