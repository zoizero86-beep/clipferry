package core

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bkrdmrcioglu/clipferry/internal/store"
)

// DataDir is where the desktop app keeps its database and log:
// $CLIPFERRY_DATA_DIR, or "Clipferry" in the user's config directory
// (~/Library/Application Support on macOS, %APPDATA% on Windows).
func DataDir() string {
	if d := os.Getenv("CLIPFERRY_DATA_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "Clipferry")
}

// History is the app's clipboard history opened without running the app,
// for the standalone MCP server. It reads settings on every call, so a
// change made in the app (such as letting AI tools read secrets) applies at
// once.
type History struct {
	st *store.Store
	// Installed is false when no Clipferry database exists on this machine;
	// the history is then empty.
	Installed bool
}

// OpenHistory opens the database in dir. A missing database is not an error:
// the history is just empty, so the server still starts (e.g. in a container).
func OpenHistory(dir string) (*History, error) {
	path := filepath.Join(dir, "clipferry.db")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		st, err := store.Open(":memory:")
		if err != nil {
			return nil, err
		}
		return &History{st: st}, nil
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	return &History{st: st, Installed: true}, nil
}

// Store implements MCPSource.
func (h *History) Store() *store.Store { return h.st }

// SelfID implements MCPSource.
func (h *History) SelfID() string {
	id, _ := h.st.Setting("device_id", "")
	return id
}

// ShowSecrets implements MCPSource.
func (h *History) ShowSecrets() bool {
	raw, _ := h.st.Setting("settings", "")
	var s Settings
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil {
		return false
	}
	return s.MCPShowSecrets
}

// Close closes the database.
func (h *History) Close() error { return h.st.Close() }
