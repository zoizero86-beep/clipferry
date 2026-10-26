package core

// End-to-end test: two simulated devices (fake clipboards, real engines,
// real local API and MCP endpoint) pair through a real relay with a code and
// exchange clips. Run with: go test ./internal/core -run TestEndToEnd -v

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/relay"
	"github.com/bkrdmrcioglu/clipferry/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeClipboard struct {
	mu   sync.Mutex
	text string
}

func (f *fakeClipboard) Text() (string, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.text, nil }
func (f *fakeClipboard) SetText(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.text = s
	return nil
}

type device struct {
	name string
	cb   *fakeClipboard
	e    *Engine
	api  *httptest.Server
	ev   <-chan Event
}

func newDevice(t *testing.T, ctx context.Context, name, platform, relayURL string) *device {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cb := &fakeClipboard{}
	e, err := New(Options{Store: st, Clipboard: cb, DeviceName: name, Platform: platform, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s := e.Settings()
	s.RelayURL = relayURL
	if _, err := e.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	ev, stop := e.Subscribe()
	t.Cleanup(stop)
	go e.Run(ctx)
	api := httptest.NewServer(localOnly(e.Handler()))
	t.Cleanup(api.Close)
	return &device{name: name, cb: cb, e: e, api: api, ev: ev}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitEvent(t *testing.T, d *device, typ string) Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-d.ev:
			if ev.Type == typ {
				return ev
			}
			if ev.Type == "pair.failed" && typ != "pair.failed" {
				t.Fatalf("%s: pairing failed: %v", d.name, ev.Data)
			}
		case <-timeout:
			t.Fatalf("%s: timed out waiting for event %s", d.name, typ)
		}
	}
}

func clipsOf(t *testing.T, d *device) []store.Clip {
	t.Helper()
	cs, err := d.e.Store().ListClips(store.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func mcpClient(t *testing.T, ctx context.Context, d *device, token string) *mcp.ClientSession {
	t.Helper()
	httpClient := &http.Client{Transport: bearer{token}}
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: d.api.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func callText(t *testing.T, ctx context.Context, s *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := relay.New(relay.Options{})
	go rl.Run(ctx)
	relaySrv := httptest.NewServer(rl)
	defer relaySrv.Close()

	mac := newDevice(t, ctx, "Bekir's MacBook", "macos", relaySrv.URL)
	win := newDevice(t, ctx, "Office PC", "windows", relaySrv.URL)

	// --- pairing with a code -------------------------------------------------
	ps, err := mac.e.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := win.e.JoinPairing(ps.Code); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, mac, "pair.done")
	waitEvent(t, win, "pair.done")
	waitFor(t, "both devices connected", func() bool { return mac.e.Status().Connected && win.e.Status().Connected })
	waitFor(t, "devices to know each other", func() bool {
		md, _ := mac.e.Store().Devices()
		wd, _ := win.e.Store().Devices()
		return len(md) == 2 && len(wd) == 2
	})
	t.Logf("✓ paired with code %s", ps.Code)

	// --- a clip copied on the Mac appears on the PC --------------------------
	mac.cb.SetText("docker compose up -d")
	waitFor(t, "clip on windows clipboard", func() bool { s, _ := win.cb.Text(); return s == "docker compose up -d" })
	wc := clipsOf(t, win)
	if len(wc) != 1 || wc[0].DeviceName != "Bekir's MacBook" || wc[0].Platform != "macos" {
		t.Fatalf("windows history = %+v", wc)
	}
	time.Sleep(200 * time.Millisecond) // give an echo the chance to happen
	if n := len(clipsOf(t, mac)); n != 1 {
		t.Fatalf("mac has %d clips, want 1 (echo loop?)", n)
	}
	if n := len(clipsOf(t, win)); n != 1 {
		t.Fatalf("windows has %d clips, want 1 (re-captured a remote clip?)", n)
	}
	t.Log("✓ mac → windows, no echo")

	// --- and back ------------------------------------------------------------
	win.cb.SetText("SELECT * FROM orders WHERE status = 'paid';")
	waitFor(t, "clip on mac clipboard", func() bool { s, _ := mac.cb.Text(); return strings.HasPrefix(s, "SELECT * FROM orders") })
	t.Log("✓ windows → mac")

	// --- secrets: expire mode (default) syncs, marks and later removes -------
	token := "ghp_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"
	mac.cb.SetText(token)
	waitFor(t, "secret on windows", func() bool { s, _ := win.cb.Text(); return s == token })
	sec := clipsOf(t, win)[0]
	if !sec.Sensitive || sec.SecretKind != "github-token" || sec.ExpiresAt == nil {
		t.Fatalf("secret clip not marked: %+v", sec)
	}
	t.Log("✓ secret synced as sensitive with expiry")

	// --- secrets: skip mode keeps them on the device -------------------------
	ws := win.e.Settings()
	ws.SecretMode = SecretSkip
	if _, err := win.e.UpdateSettings(ws); err != nil {
		t.Fatal(err)
	}
	win.cb.SetText("AKIAIOSFODNN7EXAMPLE")
	waitEvent(t, win, "secret.skipped")
	time.Sleep(300 * time.Millisecond)
	for _, d := range []*device{mac, win} {
		for _, c := range clipsOf(t, d) {
			if strings.Contains(c.Content, "AKIA") {
				t.Fatalf("%s stored a skipped secret", d.name)
			}
		}
	}
	if s, _ := mac.cb.Text(); s == "AKIAIOSFODNN7EXAMPLE" {
		t.Fatal("skipped secret reached the mac clipboard")
	}
	t.Log("✓ skip mode withholds secrets")

	// --- MCP on the PC: "what did I just copy on my other machine?" ----------
	unauth, _ := http.Post(win.api.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mcp without token: %d", unauth.StatusCode)
	}
	s := mcpClient(t, ctx, win, win.e.Token())
	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "get_latest_clip") || !strings.Contains(got, "search_clips") || !strings.Contains(got, "list_recent_clips") {
		t.Fatalf("tools = %s", got)
	}
	latest := callText(t, ctx, s, "get_latest_clip", map[string]any{"from_other_device": true})
	if !strings.Contains(latest, "withheld") || strings.Contains(latest, token) {
		t.Fatalf("latest clip leaked the secret over MCP: %s", latest)
	}
	found := callText(t, ctx, s, "search_clips", map[string]any{"query": "compose"})
	if !strings.Contains(found, "docker compose up -d") || !strings.Contains(found, "Bekir's MacBook") {
		t.Fatalf("search_clips = %s", found)
	}
	recent := callText(t, ctx, s, "list_recent_clips", map[string]any{"device": "Office PC"})
	if !strings.Contains(recent, "SELECT * FROM orders") || strings.Contains(recent, "docker compose") {
		t.Fatalf("list_recent_clips(device) = %s", recent)
	}
	t.Log("✓ MCP tools answer on the other device, secrets withheld")

	// --- expiry removes the secret from history and clipboard everywhere -----
	later := time.Now().Add(10 * time.Minute)
	win.e.expire(later)
	mac.e.expire(later)
	if s, _ := win.cb.Text(); s == token {
		t.Fatal("expired secret still on windows clipboard")
	}
	for _, d := range []*device{mac, win} {
		for _, c := range clipsOf(t, d) {
			if c.Sensitive {
				t.Fatalf("%s still has the expired secret", d.name)
			}
		}
	}
	t.Log("✓ expired secret removed from history and clipboard")

	// --- deleting on one device deletes everywhere; pins stay local ----------
	target := clipsOf(t, mac)
	var dockerID string
	for _, c := range target {
		if c.Content == "docker compose up -d" {
			dockerID = c.ID
		}
	}
	if err := mac.e.Delete(dockerID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delete to reach windows", func() bool { c, _ := win.e.Store().GetClip(dockerID); return c == nil })
	t.Log("✓ delete propagates")

	// --- a wrong code fails cleanly ------------------------------------------
	other := newDevice(t, ctx, "Stranger", "linux", relaySrv.URL)
	ps2, err := mac.e.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	wrong := ps2.Code[:4] + fmt.Sprintf("%03d", (atoi(ps2.Code[4:7])+1)%1000) + ps2.Code[7:]
	if _, err := other.e.JoinPairing(wrong); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, other, "pair.failed")
	if other.e.Status().Paired {
		t.Fatal("a device with the wrong code joined the ferry")
	}
	t.Log("✓ wrong code is rejected")

	// --- the local API is token-protected and refuses foreign hosts ----------
	resp, _ := http.Get(mac.api.URL + "/api/clips")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api without token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", mac.api.URL+"/api/clips", nil)
	req.Host = "evil.example"
	req.Header.Set("Authorization", "Bearer "+mac.e.Token())
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("api with foreign Host: %d", resp.StatusCode)
	}
	t.Log("✓ local API requires the token and a loopback Host")
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
