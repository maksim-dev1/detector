// Package store is the SQLite-backed persistence for bot users and their
// camera streams. One file, no server; safe for the handful of concurrent
// callers the bot has.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// User status values.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusBlocked  = "blocked"
)

type User struct {
	ChatID    int64
	Username  string
	Status    string
	IsAdmin   bool
	CreatedAt time.Time
}

type Stream struct {
	ID         int64
	Owner      int64
	Name       string
	URL        string
	Rotate     string
	Classes    []string
	Conf       float32
	Enabled    bool
	MutedUntil time.Time // events suppressed until this instant (zero = not muted)
	CreatedAt  time.Time
}

// Muted reports whether the stream's notifications are currently suppressed.
func (s Stream) Muted() bool { return time.Now().Before(s.MutedUntil) }

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS users (
  chat_id    INTEGER PRIMARY KEY,
  username   TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'pending',
  is_admin   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS streams (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  owner      INTEGER NOT NULL,
  name       TEXT NOT NULL,
  url        TEXT NOT NULL,
  rotate     TEXT NOT NULL DEFAULT 'none',
  classes    TEXT NOT NULL DEFAULT 'person',
  conf       REAL NOT NULL DEFAULT 0.35,
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_streams_owner ON streams(owner);
`

// migrations applied after schema; each ignores "duplicate column" errors.
var migrations = []string{
	`ALTER TABLE streams ADD COLUMN muted_until INTEGER NOT NULL DEFAULT 0`,
}

// Open opens (creating if needed) the SQLite file at path and runs migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // modernc sqlite: serialize writers, avoid SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate %q: %w", m, err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// --- users ---

// UpsertUser inserts a new pending user, or refreshes the username of an
// existing one. Returns the current row.
func (s *Store) UpsertUser(chatID int64, username string) (User, error) {
	_, err := s.db.Exec(`
		INSERT INTO users (chat_id, username, created_at) VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET username=excluded.username`,
		chatID, username, time.Now().Unix())
	if err != nil {
		return User{}, err
	}
	u, _, err := s.GetUser(chatID)
	return u, err
}

func (s *Store) GetUser(chatID int64) (User, bool, error) {
	var u User
	var created int64
	var admin int
	err := s.db.QueryRow(`SELECT chat_id, username, status, is_admin, created_at FROM users WHERE chat_id=?`, chatID).
		Scan(&u.ChatID, &u.Username, &u.Status, &admin, &created)
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	u.IsAdmin = admin != 0
	u.CreatedAt = time.Unix(created, 0)
	return u, true, nil
}

func (s *Store) SetUserStatus(chatID int64, status string) error {
	_, err := s.db.Exec(`UPDATE users SET status=? WHERE chat_id=?`, status, chatID)
	return err
}

// EnsureAdmin makes chatID an approved admin, creating the row if absent.
func (s *Store) EnsureAdmin(chatID int64) error {
	_, err := s.db.Exec(`
		INSERT INTO users (chat_id, status, is_admin, created_at) VALUES (?, 'approved', 1, ?)
		ON CONFLICT(chat_id) DO UPDATE SET status='approved', is_admin=1`,
		chatID, time.Now().Unix())
	return err
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT chat_id, username, status, is_admin, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created int64
		var admin int
		if err := rows.Scan(&u.ChatID, &u.Username, &u.Status, &admin, &created); err != nil {
			return nil, err
		}
		u.IsAdmin = admin != 0
		u.CreatedAt = time.Unix(created, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- streams ---

func (s *Store) AddStream(st Stream) (Stream, error) {
	if st.Rotate == "" {
		st.Rotate = "none"
	}
	if len(st.Classes) == 0 {
		st.Classes = []string{"person"}
	}
	if st.Conf == 0 {
		st.Conf = 0.35
	}
	now := time.Now()
	res, err := s.db.Exec(`
		INSERT INTO streams (owner, name, url, rotate, classes, conf, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		st.Owner, st.Name, st.URL, st.Rotate, strings.Join(st.Classes, ","), st.Conf,
		b2i(st.Enabled), now.Unix())
	if err != nil {
		return Stream{}, err
	}
	st.ID, _ = res.LastInsertId()
	st.CreatedAt = now
	return st, nil
}

func (s *Store) GetStream(id int64) (Stream, bool, error) {
	row := s.db.QueryRow(`SELECT id, owner, name, url, rotate, classes, conf, enabled, muted_until, created_at FROM streams WHERE id=?`, id)
	st, err := scanStream(row)
	if err == sql.ErrNoRows {
		return Stream{}, false, nil
	}
	return st, err == nil, err
}

func (s *Store) ListStreams() ([]Stream, error) { return s.queryStreams(``) }
func (s *Store) ListStreamsByOwner(o int64) ([]Stream, error) {
	return s.queryStreams(` WHERE owner=?`, o)
}

func (s *Store) CountStreamsByOwner(o int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM streams WHERE owner=?`, o).Scan(&n)
	return n, err
}

func (s *Store) UpdateStream(st Stream) error {
	_, err := s.db.Exec(`
		UPDATE streams SET name=?, url=?, rotate=?, classes=?, conf=?, enabled=? WHERE id=?`,
		st.Name, st.URL, st.Rotate, strings.Join(st.Classes, ","), st.Conf, b2i(st.Enabled), st.ID)
	return err
}

func (s *Store) SetStreamEnabled(id int64, on bool) error {
	_, err := s.db.Exec(`UPDATE streams SET enabled=? WHERE id=?`, b2i(on), id)
	return err
}

func (s *Store) DeleteStream(id int64) error {
	_, err := s.db.Exec(`DELETE FROM streams WHERE id=?`, id)
	return err
}

func (s *Store) queryStreams(where string, args ...any) ([]Stream, error) {
	rows, err := s.db.Query(`SELECT id, owner, name, url, rotate, classes, conf, enabled, muted_until, created_at FROM streams`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stream
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanStream(r scanner) (Stream, error) {
	var st Stream
	var classes string
	var enabled int
	var muted, created int64
	if err := r.Scan(&st.ID, &st.Owner, &st.Name, &st.URL, &st.Rotate, &classes, &st.Conf, &enabled, &muted, &created); err != nil {
		return Stream{}, err
	}
	if classes != "" {
		st.Classes = strings.Split(classes, ",")
	}
	st.Enabled = enabled != 0
	if muted > 0 {
		st.MutedUntil = time.Unix(muted, 0)
	}
	st.CreatedAt = time.Unix(created, 0)
	return st, nil
}

// MuteStream suppresses the stream's notifications until t (pass a past time to
// unmute).
func (s *Store) MuteStream(id int64, t time.Time) error {
	_, err := s.db.Exec(`UPDATE streams SET muted_until=? WHERE id=?`, t.Unix(), id)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
