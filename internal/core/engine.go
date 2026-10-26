// Package core is the Clipferry engine: it watches the system clipboard,
// filters secrets, keeps the local history and exchanges encrypted clips with
// the other devices of the ferry. The desktop app is a thin shell around it.
package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/box"
	"github.com/bkrdmrcioglu/clipferry/internal/ferry"
	"github.com/bkrdmrcioglu/clipferry/internal/lan"
	"github.com/bkrdmrcioglu/clipferry/internal/secrets"
	"github.com/bkrdmrcioglu/clipferry/internal/store"
	"github.com/coder/websocket"
)

// Version is the app version, overridden at build time.
var Version = "0.1.0"

// Devices sync over the local network unless a relay server is configured
// (Settings.RelayURL), which also works across networks.

// MaxClipBytes caps the size of a synced clip.
const MaxClipBytes = 1 << 20

// Clipboard is the system clipboard. Implementations must be safe for
// concurrent use.
type Clipboard interface {
	Text() (string, error)
	SetText(string) error
}

// Optional clipboard capabilities, used when the implementation has them.
type changeCounter interface{ ChangeCount() int64 }
type concealer interface{ Concealed() bool }

// Autostarter registers the app to start when the user logs in. The desktop
// shell provides it; the engine only exposes it to the UI.
type Autostarter interface {
	IsEnabled() (bool, error)
	Enable() error
	Disable() error
}

// Secret handling modes.
const (
	SecretExpire = "expire" // sync, mark sensitive, delete everywhere after a TTL
	SecretSkip   = "skip"   // keep it on this device's clipboard only
	SecretSync   = "sync"   // treat like any other clip (still marked sensitive)
)

// Settings are user preferences, stored locally.
type Settings struct {
	DeviceName       string `json:"device_name"`
	SecretMode       string `json:"secret_mode"`
	SecretTTLSeconds int    `json:"secret_ttl_seconds"`
	ApplyRemote      bool   `json:"apply_remote"`
	Paused           bool   `json:"paused"`
	HistoryLimit     int    `json:"history_limit"`
	RelayURL         string `json:"relay_url"`
	RelayToken       string `json:"relay_token"`
	MCPShowSecrets   bool   `json:"mcp_show_secrets"`
	Language         string `json:"language"`    // "auto", "en" or "tr"
	AutoUpdate       bool   `json:"auto_update"` // install new versions automatically
}

// Options configures an Engine.
type Options struct {
	Store        *store.Store
	Clipboard    Clipboard
	DeviceName   string        // default name for this device
	Platform     string        // "macos", "windows", ...; defaults to runtime.GOOS
	PollInterval time.Duration // clipboard polling; default 400ms
	Logf         func(format string, args ...any)

	// Discovery finds devices on the local network; nil disables
	// same-network mode, leaving only relay servers.
	Discovery lan.Discovery
	// LANListen is the address of the same-network relay; default ":47824".
	LANListen string
}

// Event is pushed to the UI.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// Status describes the connection to the ferry.
type Status struct {
	Mode      string `json:"mode"` // "lan" or "relay"
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// PairState describes a pairing in progress.
type PairState struct {
	Role      string     `json:"role"` // "host" or "join"
	Code      string     `json:"code,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Engine is the running Clipferry core.
type Engine struct {
	opts  Options
	st    *store.Store
	cb    Clipboard
	self  ferry.Peer
	token string // local API / MCP token

	mu        sync.Mutex
	settings  Settings
	ferryKey  []byte
	status    Status
	pair      *PairState
	pairStop  context.CancelFunc
	lastSeen  string // hash of the clipboard text last observed or written
	lastCount int64  // clipboard change counter at the last poll
	connStop  context.CancelFunc
	lastHello time.Time

	out  chan []byte
	subs map[chan Event]struct{}
	ctx  context.Context
	lan  *lanState // nil without Discovery

	autostart Autostarter
	updater   *updater
}

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newID() string { return strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + randomHex(6) }

// PlatformName maps GOOS to the name shown in the UI.
func PlatformName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}

// DefaultDeviceName is the name the user gave this computer: the macOS
// "Computer Name" (e.g. "Bekir's MacBook Pro"), otherwise the host name
// without any DNS suffix a router may have appended.
func DefaultDeviceName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "My computer"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// New creates an engine, generating this device's identity on first run.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil || opts.Clipboard == nil {
		return nil, errors.New("core: Store and Clipboard are required")
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 400 * time.Millisecond
	}
	if opts.Platform == "" {
		opts.Platform = PlatformName()
	}
	if opts.DeviceName == "" {
		opts.DeviceName = DefaultDeviceName()
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	e := &Engine{opts: opts, st: opts.Store, cb: opts.Clipboard, out: make(chan []byte, 256), subs: map[chan Event]struct{}{}}

	get := func(key string, gen func() string) (string, error) {
		v, err := e.st.Setting(key, "")
		if err != nil || v != "" {
			return v, err
		}
		v = gen()
		return v, e.st.SetSetting(key, v)
	}
	id, err := get("device_id", func() string { return randomHex(8) })
	if err != nil {
		return nil, err
	}
	if e.token, err = get("local_token", func() string { return randomHex(24) }); err != nil {
		return nil, err
	}
	if err := e.loadSettings(); err != nil {
		return nil, err
	}
	if k, _ := e.st.Setting("ferry_key", ""); k != "" {
		if e.ferryKey, err = hex.DecodeString(k); err != nil || len(e.ferryKey) != box.KeySize {
			return nil, errors.New("core: stored ferry key is corrupt")
		}
	}
	e.self = ferry.Peer{ID: id, Name: e.settings.DeviceName, Platform: opts.Platform}
	if err := e.openLAN(); err != nil {
		e.opts.Logf("same-network mode unavailable: %v", err)
	}
	e.status.Paired = e.ferryKey != nil
	if err := e.st.UpsertDevice(store.Device{ID: id, Name: e.self.Name, Platform: e.self.Platform, LastSeen: time.Now(), Self: true}); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) loadSettings() error {
	s := Settings{
		DeviceName:       e.opts.DeviceName,
		SecretMode:       SecretExpire,
		SecretTTLSeconds: 300,
		ApplyRemote:      true,
		HistoryLimit:     500,
		Language:         "auto",
		AutoUpdate:       true,
	}
	if raw, err := e.st.Setting("settings", ""); err != nil {
		return err
	} else if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return fmt.Errorf("core: settings: %w", err)
		}
	}
	e.settings = s
	return nil
}

// Token is the bearer token for the local API and MCP endpoint.
func (e *Engine) Token() string { return e.token }

// Self returns this device's identity.
func (e *Engine) Self() ferry.Peer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.self
}

// Settings returns a copy of the current settings.
func (e *Engine) Settings() Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.settings
}

// UpdateSettings validates and stores new settings.
func (e *Engine) UpdateSettings(s Settings) (Settings, error) {
	s.DeviceName = strings.TrimSpace(s.DeviceName)
	if s.DeviceName == "" || len(s.DeviceName) > 64 {
		return Settings{}, errors.New("device name must be 1–64 characters")
	}
	switch s.SecretMode {
	case SecretExpire, SecretSkip, SecretSync:
	default:
		return Settings{}, errors.New("secret_mode must be expire, skip or sync")
	}
	if s.SecretTTLSeconds < 30 || s.SecretTTLSeconds > 86400 {
		return Settings{}, errors.New("secret expiry must be between 30 seconds and 24 hours")
	}
	if s.Language == "" {
		s.Language = "auto"
	}
	if s.Language != "auto" && !languages[s.Language] {
		return Settings{}, errors.New("language must be auto, en or tr")
	}
	if s.HistoryLimit < 10 || s.HistoryLimit > 10000 {
		return Settings{}, errors.New("history limit must be between 10 and 10000")
	}
	s.RelayURL = strings.TrimSpace(s.RelayURL)
	if s.RelayURL != "" && !(strings.HasPrefix(s.RelayURL, "wss://") || strings.HasPrefix(s.RelayURL, "ws://") ||
		strings.HasPrefix(s.RelayURL, "https://") || strings.HasPrefix(s.RelayURL, "http://")) {
		return Settings{}, errors.New("relay URL must start with wss:// or https://")
	}
	raw, _ := json.Marshal(s)
	if err := e.st.SetSetting("settings", string(raw)); err != nil {
		return Settings{}, err
	}
	e.mu.Lock()
	old := e.settings
	e.settings = s
	e.self.Name = s.DeviceName
	self := e.self
	e.mu.Unlock()
	e.st.UpsertDevice(store.Device{ID: self.ID, Name: self.Name, Platform: self.Platform, LastSeen: time.Now(), Self: true})
	if old.RelayURL != s.RelayURL || old.RelayToken != s.RelayToken {
		e.reconnect()
	} else if old.DeviceName != s.DeviceName {
		e.sendHello()
	}
	e.emit("settings", s)
	return s, nil
}

// Status returns the current connection status.
func (e *Engine) Status() Status {
	e.mu.Lock()
	st := e.status
	e.mu.Unlock()
	st.Mode = "relay"
	if e.lanMode() {
		st.Mode = "lan"
	}
	return st
}

// Pairing returns the pairing in progress, if any.
func (e *Engine) Pairing() *PairState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pair == nil {
		return nil
	}
	p := *e.pair
	return &p
}

func (e *Engine) setStatus(connected bool, errMsg string) {
	e.mu.Lock()
	e.status = Status{Paired: e.ferryKey != nil, Connected: connected, Error: errMsg}
	e.mu.Unlock()
	e.emit("status", e.Status())
}

// ---- events ---------------------------------------------------------------

// Subscribe returns a channel of events and a function to stop listening.
func (e *Engine) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
		e.mu.Unlock()
	}
}

func (e *Engine) emit(typ string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- Event{Type: typ, Data: data}:
		default:
		}
	}
}

// ---- running --------------------------------------------------------------

// Run blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	e.mu.Lock()
	e.ctx = ctx
	if cc, ok := e.cb.(changeCounter); ok {
		e.lastCount = cc.ChangeCount()
	}
	if t, err := e.cb.Text(); err == nil {
		e.lastSeen = hashText(t) // don't import whatever was on the clipboard at launch
	}
	e.mu.Unlock()
	e.startLAN(ctx)
	e.reconnect()
	go e.updateLoop(ctx)

	poll := time.NewTicker(e.opts.PollInterval)
	janitor := time.NewTicker(5 * time.Second)
	hello := time.NewTicker(time.Minute) // also how other devices tell we are online
	defer poll.Stop()
	defer janitor.Stop()
	defer hello.Stop()
	for {
		select {
		case <-ctx.Done():
			e.mu.Lock()
			if e.connStop != nil {
				e.connStop()
			}
			if e.pairStop != nil {
				e.pairStop()
			}
			e.mu.Unlock()
			return
		case <-poll.C:
			e.pollClipboard()
		case <-janitor.C:
			e.expire(time.Now())
		case <-hello.C:
			e.sendHello()
		}
	}
}

func (e *Engine) pollClipboard() {
	if cc, ok := e.cb.(changeCounter); ok {
		n := cc.ChangeCount()
		e.mu.Lock()
		same := n == e.lastCount
		e.lastCount = n
		e.mu.Unlock()
		if same {
			return // nothing was copied since the last poll
		}
	}
	text, err := e.cb.Text()
	if err != nil || text == "" {
		return
	}
	h := hashText(text)
	e.mu.Lock()
	if h == e.lastSeen {
		e.mu.Unlock()
		return
	}
	e.lastSeen = h
	paused := e.settings.Paused
	e.mu.Unlock()
	if paused {
		return
	}
	var hint *secrets.Finding
	if c, ok := e.cb.(concealer); ok && c.Concealed() {
		hint = &secrets.Finding{Kind: "password-manager", Label: "Password from a password manager"}
	}
	if _, err := e.capture(text, hint); err != nil {
		e.opts.Logf("capture: %v", err)
	}
}

// Capture records text copied on this device and sends it to the ferry,
// applying the secret policy. It returns the stored clip, or nil when the
// text was withheld.
func (e *Engine) Capture(text string) (*store.Clip, error) { return e.capture(text, nil) }

// capture is Capture with an optional finding supplied by the clipboard
// itself (a password manager marking its content as concealed).
func (e *Engine) capture(text string, hint *secrets.Finding) (*store.Clip, error) {
	if strings.TrimSpace(text) == "" || len(text) > MaxClipBytes {
		return nil, nil
	}
	set := e.Settings()
	self := e.Self()
	c := store.Clip{
		ID: newID(), Content: text, DeviceID: self.ID, DeviceName: self.Name, Platform: self.Platform,
		CreatedAt: time.Now(),
	}
	f := hint
	if f == nil {
		f = secrets.Detect(text)
	}
	if f != nil {
		c.Sensitive, c.SecretKind = true, f.Kind
		switch set.SecretMode {
		case SecretSkip:
			e.emit("secret.skipped", map[string]string{"kind": f.Kind, "label": f.Label})
			return nil, nil
		case SecretExpire:
			t := c.CreatedAt.Add(time.Duration(set.SecretTTLSeconds) * time.Second)
			c.ExpiresAt = &t
		}
	}
	if latest, _ := e.st.LatestClip(""); latest != nil && latest.Content == text && latest.DeviceID == self.ID {
		return nil, nil // copied the same thing again
	}
	if _, err := e.st.AddClip(c); err != nil {
		return nil, err
	}
	e.emit("clip.added", c)
	e.send(wire{Type: "clip", Clip: &c})
	e.trim()
	return &c, nil
}

func (e *Engine) trim() {
	ids, err := e.st.Trim(e.Settings().HistoryLimit)
	if err == nil {
		for _, id := range ids {
			e.emit("clip.deleted", map[string]string{"id": id})
		}
	}
}

// expire deletes clips past their expiry everywhere — history and, if it is
// still there, the system clipboard.
func (e *Engine) expire(now time.Time) {
	gone, err := e.st.DeleteExpired(now)
	if err != nil || len(gone) == 0 {
		return
	}
	cur, _ := e.cb.Text()
	for _, c := range gone {
		if cur == c.Content {
			e.writeClipboard("")
		}
		e.emit("clip.deleted", map[string]string{"id": c.ID})
	}
}

func (e *Engine) writeClipboard(text string) {
	e.mu.Lock()
	e.lastSeen = hashText(text)
	e.mu.Unlock()
	if err := e.cb.SetText(text); err != nil {
		e.opts.Logf("clipboard write: %v", err)
	}
}

// ---- user actions ---------------------------------------------------------

// Copy puts a clip from history back on this device's clipboard without
// creating a new history entry.
func (e *Engine) Copy(id string) error {
	c, err := e.st.GetClip(id)
	if err != nil {
		return err
	}
	if c == nil {
		return ErrNotFound
	}
	e.writeClipboard(c.Content)
	return nil
}

// ErrNotFound is returned for unknown clip ids.
var ErrNotFound = errors.New("not found")

// Pin pins or unpins a clip on this device.
func (e *Engine) Pin(id string, pinned bool) error {
	ok, err := e.st.SetPinned(id, pinned)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if c, _ := e.st.GetClip(id); c != nil {
		e.emit("clip.updated", c)
	}
	return nil
}

// Delete removes a clip on every device of the ferry.
func (e *Engine) Delete(id string) error {
	ok, err := e.st.DeleteClip(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	e.emit("clip.deleted", map[string]string{"id": id})
	e.send(wire{Type: "delete", IDs: []string{id}})
	return nil
}

// Clear removes every unpinned clip on every device of the ferry.
func (e *Engine) Clear() (int, error) {
	ids, err := e.st.ClearUnpinned()
	if err != nil {
		return 0, err
	}
	if len(ids) > 0 {
		e.emit("clips.cleared", map[string][]string{"ids": ids})
		e.send(wire{Type: "delete", IDs: ids})
	}
	return len(ids), nil
}

// ---- ferry ----------------------------------------------------------------

type wire struct {
	V     int         `json:"v"`
	Type  string      `json:"type"` // clip | delete | hello
	From  ferry.Peer  `json:"from"`
	At    time.Time   `json:"at"`
	Clip  *store.Clip `json:"clip,omitempty"`
	IDs   []string    `json:"ids,omitempty"`
	Reply bool        `json:"reply,omitempty"`
	Join  bool        `json:"join,omitempty"` // first hello on a new connection
}

func (e *Engine) relay() ferry.Relay {
	s := e.Settings()
	return ferry.Relay{URL: s.RelayURL, Token: s.RelayToken}
}

func (e *Engine) send(w wire) {
	e.mu.Lock()
	key := e.ferryKey
	w.V, w.From, w.At = 1, e.self, time.Now()
	e.mu.Unlock()
	if key == nil {
		return
	}
	if w.Clip != nil {
		c := *w.Clip
		c.Pinned = false // pins are per device
		w.Clip = &c
	}
	plain, _ := json.Marshal(w)
	sealed, err := box.Seal(box.Derive(key, "clips"), plain)
	if err != nil {
		return
	}
	select {
	case e.out <- sealed:
	default:
		e.opts.Logf("outbox full, dropping %s", w.Type)
	}
}

func (e *Engine) sendHello() {
	e.mu.Lock()
	e.lastHello = time.Now()
	e.mu.Unlock()
	e.send(wire{Type: "hello"})
}

// reconnect (re)starts the relay connection for the current ferry key.
func (e *Engine) reconnect() {
	e.mu.Lock()
	if e.connStop != nil {
		e.connStop()
		e.connStop = nil
	}
	if e.ctx == nil {
		e.mu.Unlock()
		return
	}
	key := e.ferryKey
	var ctx context.Context
	if key != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(e.ctx)
		e.connStop = cancel
	}
	e.mu.Unlock()
	e.updateAdvert(key)
	if key != nil {
		go e.connLoop(ctx, key)
	}
}

func (e *Engine) connLoop(ctx context.Context, key []byte) {
	backoff := time.Second
	room := box.RoomID(key)
	clipKey := box.Derive(key, "clips")
	for ctx.Err() == nil {
		target, peerID := e.dialTarget(key)
		c, err := target.Dial(ctx, room, false)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if peerID != "" {
				// That device is announced but unreachable (asleep, firewall):
				// meet somewhere else for now.
				e.forgetPeer(peerID)
				e.setStatus(false, "can't reach "+e.deviceName(peerID)+" on this network — is Clipferry allowed through its firewall?")
				continue
			}
			e.setStatus(false, err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		e.setStatus(true, "")
		// Announce ourselves and offer what we copied while disconnected.
		e.mu.Lock()
		e.lastHello = time.Now()
		e.mu.Unlock()
		e.send(wire{Type: "hello", Join: true})
		e.resendRecent()
		e.serve(ctx, c, clipKey)
		c.CloseNow()
		if ctx.Err() == nil {
			e.setStatus(false, "connection to relay lost, retrying")
		}
	}
}

func (e *Engine) serve(ctx context.Context, c *websocket.Conn, clipKey []byte) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			plain, err := box.Open(clipKey, data)
			if err != nil {
				continue // not for us, or tampered with
			}
			var w wire
			if json.Unmarshal(plain, &w) == nil {
				e.receive(w)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-e.out:
			wctx, wcancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.Write(wctx, websocket.MessageBinary, msg)
			wcancel()
			if err != nil {
				// Put it back so it goes out after reconnecting.
				select {
				case e.out <- msg:
				default:
				}
				return
			}
		}
	}
}

func (e *Engine) receive(w wire) {
	self := e.Self()
	if w.From.ID == "" || w.From.ID == self.ID {
		return
	}
	if err := e.st.UpsertDevice(store.Device{ID: w.From.ID, Name: w.From.Name, Platform: w.From.Platform, LastSeen: w.At}); err == nil {
		if ds, err := e.st.Devices(); err == nil {
			e.emit("devices", ds)
		}
	}
	switch w.Type {
	case "hello":
		if w.Join && time.Since(w.At) < time.Minute {
			// The sender just (re)connected and may have missed what we copied.
			e.resendRecent()
		}
		// Answer so the newcomer learns about us, but don't ping-pong.
		e.mu.Lock()
		recent := time.Since(e.lastHello) < 3*time.Second
		e.mu.Unlock()
		if !w.Reply && !recent && time.Since(w.At) < time.Minute {
			e.mu.Lock()
			e.lastHello = time.Now()
			e.mu.Unlock()
			e.send(wire{Type: "hello", Reply: true})
		}
	case "clip":
		if w.Clip == nil || len(w.Clip.Content) > MaxClipBytes {
			return
		}
		c := *w.Clip
		c.Pinned = false
		c.DeviceID, c.DeviceName, c.Platform = w.From.ID, w.From.Name, w.From.Platform
		if c.ExpiresAt != nil && !c.ExpiresAt.After(time.Now()) {
			return
		}
		added, err := e.st.AddClip(c)
		if err != nil || !added {
			return
		}
		e.emit("clip.added", c)
		e.trim()
		set := e.Settings()
		// Only the newest clip goes onto the clipboard; replayed backlog from
		// while we were offline just lands in history.
		if set.ApplyRemote && !set.Paused && time.Since(c.CreatedAt) < 2*time.Minute {
			if latest, _ := e.st.LatestClip(""); latest != nil && latest.ID == c.ID {
				e.writeClipboard(c.Content)
			}
		}
	case "delete":
		cur, _ := e.cb.Text()
		for _, id := range w.IDs {
			c, _ := e.st.GetClip(id)
			if c == nil || c.Pinned {
				continue
			}
			if ok, _ := e.st.DeleteClip(id); ok {
				if c.Sensitive && cur == c.Content {
					e.writeClipboard("")
				}
				e.emit("clip.deleted", map[string]string{"id": id})
			}
		}
	}
}

// ---- pairing --------------------------------------------------------------

// StartPairing shows a code another device can enter. It creates the ferry
// if this device is not in one yet.
func (e *Engine) StartPairing() (PairState, error) {
	e.mu.Lock()
	if e.ctx == nil {
		e.mu.Unlock()
		return PairState{}, errors.New("engine is not running")
	}
	if e.pairStop != nil {
		e.pairStop()
	}
	created := false
	if e.ferryKey == nil {
		e.ferryKey = box.NewKey()
		created = true
	}
	key := e.ferryKey
	exp := time.Now().Add(ferry.CodeTTL)
	ps := PairState{Role: "host", Code: ferry.NewCode(), ExpiresAt: &exp}
	ctx, cancel := context.WithCancel(e.ctx)
	e.pair, e.pairStop = &ps, cancel
	self := e.self
	e.mu.Unlock()

	if created {
		if err := e.st.SetSetting("ferry_key", hex.EncodeToString(key)); err != nil {
			cancel()
			return PairState{}, err
		}
		e.reconnect()
	}
	e.emit("pair", ps)
	go func() {
		r := e.relay()
		if e.lanMode() {
			r = ferry.Relay{URL: e.loopbackURL()}
		}
		stopAdvert := e.advertisePairing(ps.Code)
		peer, err := ferry.Host(ctx, r, ps.Code, key, self)
		stopAdvert()
		e.finishPairing(ctx, peer, err)
	}()
	return ps, nil
}

// JoinPairing enters a code shown on another device and joins its ferry.
func (e *Engine) JoinPairing(code string) (PairState, error) {
	if _, ok := ferry.NormalizeCode(code); !ok {
		return PairState{}, errors.New("a pairing code has nine digits, like 123-456-789")
	}
	e.mu.Lock()
	if e.ctx == nil {
		e.mu.Unlock()
		return PairState{}, errors.New("engine is not running")
	}
	if e.pairStop != nil {
		e.pairStop()
	}
	ps := PairState{Role: "join"}
	ctx, cancel := context.WithCancel(e.ctx)
	e.pair, e.pairStop = &ps, cancel
	self := e.self
	e.mu.Unlock()
	e.emit("pair", ps)

	go func() {
		r, err := e.relay(), error(nil)
		if e.lanMode() {
			r, err = e.findPairHost(ctx, code)
		}
		var key []byte
		var host ferry.Peer
		if err == nil {
			key, host, err = ferry.Join(ctx, r, code, self)
		}
		if err == nil {
			if err = e.st.SetSetting("ferry_key", hex.EncodeToString(key)); err == nil {
				e.mu.Lock()
				e.ferryKey = key
				e.mu.Unlock()
				e.reconnect()
			}
		}
		e.finishPairing(ctx, host, err)
	}()
	return ps, nil
}

func (e *Engine) finishPairing(ctx context.Context, peer ferry.Peer, err error) {
	e.mu.Lock()
	cancelled := ctx.Err() != nil && err != nil
	if e.pairStop != nil && !cancelled {
		e.pairStop()
		e.pair, e.pairStop = nil, nil
	}
	e.mu.Unlock()
	if cancelled {
		return
	}
	if err != nil {
		e.emit("pair.failed", map[string]string{"error": err.Error()})
		return
	}
	e.st.UpsertDevice(store.Device{ID: peer.ID, Name: peer.Name, Platform: peer.Platform, LastSeen: time.Now()})
	e.setStatus(e.Status().Connected, "")
	ds, _ := e.st.Devices()
	e.emit("devices", ds)
	e.emit("pair.done", peer)
}

// CancelPairing stops a pairing in progress.
func (e *Engine) CancelPairing() {
	e.mu.Lock()
	if e.pairStop != nil {
		e.pairStop()
	}
	e.pair, e.pairStop = nil, nil
	e.mu.Unlock()
	e.emit("pair.cancelled", nil)
}

// Leave disconnects this device from its ferry. History stays on the device.
func (e *Engine) Leave() error {
	if err := e.st.DeleteSetting("ferry_key"); err != nil {
		return err
	}
	if err := e.st.ForgetPeers(); err != nil {
		return err
	}
	e.mu.Lock()
	e.ferryKey = nil
	e.mu.Unlock()
	e.reconnect()
	e.setStatus(false, "")
	ds, _ := e.st.Devices()
	e.emit("devices", ds)
	return nil
}

// resendRecent sends this device's recent clips again; receivers drop the
// ones they already have.
func (e *Engine) resendRecent() {
	clips, err := e.st.ListClips(store.Query{DeviceID: e.Self().ID, Limit: 20})
	if err != nil {
		return
	}
	for i := len(clips) - 1; i >= 0; i-- {
		if time.Since(clips[i].CreatedAt) < 24*time.Hour {
			e.send(wire{Type: "clip", Clip: &clips[i]})
		}
	}
}

func (e *Engine) deviceName(id string) string {
	if ds, err := e.st.Devices(); err == nil {
		for _, d := range ds {
			if d.ID == id {
				return d.Name
			}
		}
	}
	return "the other device"
}

// SetAutostart connects the platform's start-at-login mechanism.
func (e *Engine) SetAutostart(a Autostarter) {
	e.mu.Lock()
	e.autostart = a
	e.mu.Unlock()
}

// Autostart reports whether the app starts at login, or nil when that cannot
// be controlled here.
func (e *Engine) Autostart() *bool {
	e.mu.Lock()
	a := e.autostart
	e.mu.Unlock()
	if a == nil {
		return nil
	}
	on, err := a.IsEnabled()
	if err != nil {
		return nil
	}
	return &on
}

// SetAutostartEnabled turns starting at login on or off.
func (e *Engine) SetAutostartEnabled(on bool) error {
	e.mu.Lock()
	a := e.autostart
	e.mu.Unlock()
	if a == nil {
		return errors.New("starting at login is not available here")
	}
	var err error
	if on {
		err = a.Enable()
	} else {
		err = a.Disable()
	}
	if err != nil {
		return err
	}
	e.emit("autostart", on)
	return nil
}

// Store exposes the underlying store for read-only use by the API.
func (e *Engine) Store() *store.Store { return e.st }
