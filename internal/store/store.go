// Package store keeps a device's clip history, known devices and settings in
// a local SQLite database. Nothing here is shared with the relay.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Clip is one entry of the shared clipboard history.
type Clip struct {
	ID         string     `json:"id"`
	Content    string     `json:"content"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	Platform   string     `json:"platform"`
	CreatedAt  time.Time  `json:"created_at"`
	Pinned     bool       `json:"pinned"`
	Sensitive  bool       `json:"sensitive"`
	SecretKind string     `json:"secret_kind,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Device is a member of the ferry, including this one.
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
	LastSeen time.Time `json:"last_seen"`
	Self     bool      `json:"self"`
}

const schema = `
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS clips (
	id          TEXT PRIMARY KEY,
	content     TEXT NOT NULL,
	device_id   TEXT NOT NULL,
	device_name TEXT NOT NULL,
	platform    TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	pinned      INTEGER NOT NULL DEFAULT 0,
	sensitive   INTEGER NOT NULL DEFAULT 0,
	secret_kind TEXT NOT NULL DEFAULT '',
	expires_at  INTEGER
);
CREATE INDEX IF NOT EXISTS clips_created_idx ON clips (created_at DESC);
CREATE INDEX IF NOT EXISTS clips_expires_idx ON clips (expires_at) WHERE expires_at IS NOT NULL;

-- Trigram full-text index: substring search in any language, ranked.
CREATE VIRTUAL TABLE IF NOT EXISTS clips_fts USING fts5(content, content='clips', content_rowid='rowid', tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS clips_ai AFTER INSERT ON clips BEGIN
	INSERT INTO clips_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS clips_ad AFTER DELETE ON clips BEGIN
	INSERT INTO clips_fts(clips_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
END;

CREATE TABLE IF NOT EXISTS devices (
	id        TEXT PRIMARY KEY,
	name      TEXT NOT NULL,
	platform  TEXT NOT NULL DEFAULT '',
	last_seen INTEGER NOT NULL,
	self      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Store wraps the database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const clipCols = `id, content, device_id, device_name, platform, created_at, pinned, sensitive, secret_kind, expires_at`

func scanClips(rows *sql.Rows) ([]Clip, error) {
	defer rows.Close()
	out := []Clip{}
	for rows.Next() {
		var c Clip
		var created int64
		var expires sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Content, &c.DeviceID, &c.DeviceName, &c.Platform, &created, &c.Pinned, &c.Sensitive, &c.SecretKind, &expires); err != nil {
			return nil, err
		}
		c.CreatedAt = time.UnixMilli(created)
		if expires.Valid {
			t := time.UnixMilli(expires.Int64)
			c.ExpiresAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddClip inserts c. It reports false if a clip with the same id already
// exists — remote clips can arrive more than once.
func (s *Store) AddClip(c Clip) (bool, error) {
	var expires any
	if c.ExpiresAt != nil {
		expires = c.ExpiresAt.UnixMilli()
	}
	res, err := s.db.Exec(`INSERT INTO clips (`+clipCols+`) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		c.ID, c.Content, c.DeviceID, c.DeviceName, c.Platform, c.CreatedAt.UnixMilli(), c.Pinned, c.Sensitive, c.SecretKind, expires)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetClip returns one clip, or nil.
func (s *Store) GetClip(id string) (*Clip, error) {
	rows, err := s.db.Query(`SELECT `+clipCols+` FROM clips WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	cs, err := scanClips(rows)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// Query selects clips for listing and search.
type Query struct {
	Text       string // substring / full-text query; empty lists everything
	DeviceID   string // only clips from this device
	ExcludeDev string // skip clips from this device
	Limit      int
	PinnedOnly bool
}

// ListClips returns clips newest first. With a search text, results are
// ranked by relevance, then recency.
func (s *Store) ListClips(q Query) ([]Clip, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	var where []string
	var args []any
	where = append(where, "(c.expires_at IS NULL OR c.expires_at > ?)")
	args = append(args, time.Now().UnixMilli())
	if q.DeviceID != "" {
		where = append(where, "c.device_id = ?")
		args = append(args, q.DeviceID)
	}
	if q.ExcludeDev != "" {
		where = append(where, "c.device_id <> ?")
		args = append(args, q.ExcludeDev)
	}
	if q.PinnedOnly {
		where = append(where, "c.pinned = 1")
	}
	text := strings.TrimSpace(q.Text)
	from := "clips c"
	order := "c.created_at DESC"
	if text != "" {
		if len([]rune(text)) >= 3 {
			from = "clips c JOIN clips_fts f ON f.rowid = c.rowid"
			where = append(where, "clips_fts MATCH ?")
			args = append(args, ftsPhrase(text))
			order = "bm25(clips_fts), c.created_at DESC"
		} else {
			where = append(where, "c.content LIKE ? ESCAPE '\\'")
			args = append(args, "%"+likeEscape(text)+"%")
		}
	}
	args = append(args, q.Limit)
	rows, err := s.db.Query(`SELECT `+prefixed("c.", clipCols)+` FROM `+from+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanClips(rows)
}

// ftsPhrase turns free text into an FTS5 query: every word must appear.
func ftsPhrase(text string) string {
	var parts []string
	for _, w := range strings.Fields(text) {
		if len([]rune(w)) < 3 {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	if len(parts) == 0 {
		return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	}
	return strings.Join(parts, " AND ")
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func prefixed(p, cols string) string {
	parts := strings.Split(cols, ", ")
	for i := range parts {
		parts[i] = p + parts[i]
	}
	return strings.Join(parts, ", ")
}

// LatestClip returns the most recent live clip, or nil.
func (s *Store) LatestClip(excludeDevice string) (*Clip, error) {
	cs, err := s.ListClips(Query{ExcludeDev: excludeDevice, Limit: 1})
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// SetPinned pins or unpins a clip.
func (s *Store) SetPinned(id string, pinned bool) (bool, error) {
	res, err := s.db.Exec(`UPDATE clips SET pinned = ? WHERE id = ?`, pinned, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteClip removes a clip.
func (s *Store) DeleteClip(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM clips WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClearUnpinned deletes every clip that is not pinned and returns their ids.
func (s *Store) ClearUnpinned() ([]string, error) {
	return s.deleteReturning(`DELETE FROM clips WHERE pinned = 0 RETURNING id`)
}

// DeleteExpired removes clips whose expiry has passed and returns them, so
// the caller can also clear them from the system clipboard.
func (s *Store) DeleteExpired(now time.Time) ([]Clip, error) {
	rows, err := s.db.Query(`DELETE FROM clips WHERE expires_at IS NOT NULL AND expires_at <= ? RETURNING `+clipCols, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	return scanClips(rows)
}

// Trim keeps at most keep unpinned clips and returns the ids it removed.
func (s *Store) Trim(keep int) ([]string, error) {
	return s.deleteReturning(`DELETE FROM clips WHERE pinned = 0 AND id NOT IN (
		SELECT id FROM clips WHERE pinned = 0 ORDER BY created_at DESC LIMIT ?) RETURNING id`, keep)
}

func (s *Store) deleteReturning(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Count returns the number of live clips.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM clips WHERE expires_at IS NULL OR expires_at > ?`, time.Now().UnixMilli()).Scan(&n)
	return n, err
}

// UpsertDevice records that a device was seen.
func (s *Store) UpsertDevice(d Device) error {
	_, err := s.db.Exec(`INSERT INTO devices (id, name, platform, last_seen, self) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, platform = excluded.platform,
		last_seen = max(devices.last_seen, excluded.last_seen), self = excluded.self`,
		d.ID, d.Name, d.Platform, d.LastSeen.UnixMilli(), d.Self)
	return err
}

// Devices lists known devices, this one first.
func (s *Store) Devices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT id, name, platform, last_seen, self FROM devices ORDER BY self DESC, last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var seen int64
		if err := rows.Scan(&d.ID, &d.Name, &d.Platform, &seen, &d.Self); err != nil {
			return nil, err
		}
		d.LastSeen = time.UnixMilli(seen)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ForgetPeers removes every device except this one (used when leaving a ferry).
func (s *Store) ForgetPeers() error {
	_, err := s.db.Exec(`DELETE FROM devices WHERE self = 0`)
	return err
}

// Setting returns a stored setting, or def when unset.
func (s *Store) Setting(key, def string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}
