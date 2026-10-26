package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkrdmrcioglu/clipferry/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, src MCPSource) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := NewMCPServer(src).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStandaloneHistory(t *testing.T) {
	ctx := context.Background()

	// No app installed: the server still starts and answers.
	empty, err := OpenHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if empty.Installed {
		t.Fatal("empty directory reported as installed")
	}
	if got := callText(t, ctx, connect(t, empty), "get_latest_clip", nil); !strings.Contains(got, "empty") {
		t.Fatalf("empty history: %s", got)
	}

	// With the app's database: reads its clips and honours its secret setting.
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "clipferry.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{Store: st, Clipboard: &fakeClipboard{}, DeviceName: "Mac"})
	if err != nil {
		t.Fatal(err)
	}
	e.Capture("make deploy")
	e.Capture("ghp_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")
	st.Close()

	h, err := OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	s := connect(t, h)
	if got := callText(t, ctx, s, "search_clips", map[string]any{"query": "deploy"}); !strings.Contains(got, "make deploy") {
		t.Fatalf("search: %s", got)
	}
	if got := callText(t, ctx, s, "get_latest_clip", nil); !strings.Contains(got, "withheld") {
		t.Fatalf("secret not withheld: %s", got)
	}
}
