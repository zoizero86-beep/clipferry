// Package ferry connects a device to its ferry through a relay and pairs new
// devices with a short code.
//
// Pairing: the device that shows the code ("host") and the device that types
// it ("joiner") meet in a short-lived relay room named after the first three
// digits of the code. They run a password-authenticated key exchange (PAKE)
// using the whole code, so the relay — or anyone guessing — learns nothing it
// could use offline, and a wrong guess just fails the exchange. Over the
// resulting session key the host hands the joiner the ferry key.
package ferry

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/box"
	"github.com/coder/websocket"
	"github.com/schollz/pake/v3"
)

// Peer identifies a device to other devices.
type Peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// Relay is where devices meet.
type Relay struct {
	URL   string // ws://, wss://, http:// or https:// base URL
	Token string // only for private relays
}

func (r Relay) roomURL(room string, fresh bool) (string, error) {
	u, err := url.Parse(strings.TrimRight(r.URL, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid relay URL %q", r.URL)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("relay URL must start with wss:// or https://, got %q", r.URL)
	}
	u.Path += "/v1/rooms/" + room
	if fresh {
		u.RawQuery = "fresh=1"
	}
	return u.String(), nil
}

// Dial opens a connection to a relay room.
func (r Relay) Dial(ctx context.Context, room string, fresh bool) (*websocket.Conn, error) {
	u, err := r.roomURL(room, fresh)
	if err != nil {
		return nil, err
	}
	opts := &websocket.DialOptions{}
	if r.Token != "" {
		opts.HTTPHeader = http.Header{"Authorization": {"Bearer " + r.Token}}
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, u, opts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("the relay rejected the token")
		}
		if resp != nil && resp.StatusCode == http.StatusConflict {
			return nil, errors.New("this pairing code is already in use — start a new one")
		}
		return nil, fmt.Errorf("cannot reach relay: %w", err)
	}
	c.SetReadLimit(4 << 20)
	return c, nil
}

// ---- pairing --------------------------------------------------------------

// CodeTTL is how long a pairing code stays valid.
const CodeTTL = 5 * time.Minute

// NewCode returns a fresh nine-digit pairing code formatted as 123-456-789.
func NewCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	if err != nil {
		panic(err)
	}
	s := fmt.Sprintf("%09d", n.Int64())
	return s[:3] + "-" + s[3:6] + "-" + s[6:]
}

// NormalizeCode strips spaces and dashes; it reports false if the result is
// not nine digits.
func NormalizeCode(code string) (string, bool) {
	var b strings.Builder
	for _, r := range code {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '\t':
		default:
			return "", false
		}
	}
	s := b.String()
	return s, len(s) == 9
}

const (
	msgPakeA byte = iota + 1
	msgPakeB
	msgHello
	msgWelcome
)

const curve = "p256"

var (
	idHost = []byte("clipferry-pair-host-v1")
	idJoin = []byte("clipferry-pair-join-v1")
)

// ErrWrongCode means the other side used a different code.
var ErrWrongCode = errors.New("the code did not match — check it and try again")

type welcome struct {
	Host     Peer   `json:"host"`
	FerryKey []byte `json:"ferry_key"`
}

func pairRoom(digits string) string { return "pair-" + digits[:3] }

func readKind(ctx context.Context, c *websocket.Conn, want byte) ([]byte, error) {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, errors.New("pairing timed out")
			}
			return nil, err
		}
		if typ == websocket.MessageBinary && len(data) > 0 && data[0] == want {
			return data[1:], nil
		}
	}
}

func write(ctx context.Context, c *websocket.Conn, kind byte, payload []byte) error {
	return c.Write(ctx, websocket.MessageBinary, append([]byte{kind}, payload...))
}

func sessionKey(p *pake.Pake) ([]byte, error) {
	k, err := p.SessionKey()
	if err != nil {
		return nil, err
	}
	return box.Derive(k, "pair"), nil
}

// Host waits for one device to join with code and gives it ferryKey.
// It returns the joiner's identity.
func Host(ctx context.Context, r Relay, code string, ferryKey []byte, self Peer) (Peer, error) {
	digits, ok := NormalizeCode(code)
	if !ok {
		return Peer{}, errors.New("invalid code")
	}
	ctx, cancel := context.WithTimeout(ctx, CodeTTL)
	defer cancel()
	c, err := r.Dial(ctx, pairRoom(digits), true)
	if err != nil {
		return Peer{}, err
	}
	defer c.CloseNow()

	a, err := pake.InitCurveWithIdentities([]byte(digits), 0, curve, idHost, idJoin)
	if err != nil {
		return Peer{}, err
	}
	if err := write(ctx, c, msgPakeA, a.Bytes()); err != nil {
		return Peer{}, err
	}
	bBytes, err := readKind(ctx, c, msgPakeB)
	if err != nil {
		return Peer{}, err
	}
	if err := a.Update(bBytes); err != nil {
		return Peer{}, ErrWrongCode
	}
	key, err := sessionKey(a)
	if err != nil {
		return Peer{}, err
	}
	sealedHello, err := readKind(ctx, c, msgHello)
	if err != nil {
		return Peer{}, err
	}
	helloJSON, err := box.Open(key, sealedHello)
	if err != nil {
		// Tell the joiner right away instead of letting it time out. The code
		// is spent either way: one guess per code.
		write(ctx, c, msgWelcome, []byte("mismatch"))
		c.Close(websocket.StatusNormalClosure, "wrong code")
		return Peer{}, errors.New("a device tried to join with a different code — start pairing again")
	}
	var joiner Peer
	if err := json.Unmarshal(helloJSON, &joiner); err != nil || joiner.ID == "" {
		return Peer{}, errors.New("pairing: malformed hello")
	}
	w, _ := json.Marshal(welcome{Host: self, FerryKey: ferryKey})
	sealed, err := box.Seal(key, w)
	if err != nil {
		return Peer{}, err
	}
	if err := write(ctx, c, msgWelcome, sealed); err != nil {
		return Peer{}, err
	}
	// Give the frame a moment to leave before the connection closes.
	c.Close(websocket.StatusNormalClosure, "paired")
	return joiner, nil
}

// Join pairs with the device showing code and returns the ferry key and the
// host's identity.
func Join(ctx context.Context, r Relay, code string, self Peer) ([]byte, Peer, error) {
	digits, ok := NormalizeCode(code)
	if !ok {
		return nil, Peer{}, errors.New("a pairing code has nine digits, like 123-456-789")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := r.Dial(ctx, pairRoom(digits), false)
	if err != nil {
		return nil, Peer{}, err
	}
	defer c.CloseNow()

	aBytes, err := readKind(ctx, c, msgPakeA)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			return nil, Peer{}, errors.New("no device is showing this code — check that it is still open on the other device")
		}
		return nil, Peer{}, err
	}
	b, err := pake.InitCurveWithIdentities([]byte(digits), 1, curve, idHost, idJoin)
	if err != nil {
		return nil, Peer{}, err
	}
	if err := b.Update(aBytes); err != nil {
		return nil, Peer{}, ErrWrongCode
	}
	if err := write(ctx, c, msgPakeB, b.Bytes()); err != nil {
		return nil, Peer{}, err
	}
	key, err := sessionKey(b)
	if err != nil {
		return nil, Peer{}, err
	}
	hello, _ := json.Marshal(self)
	sealed, err := box.Seal(key, hello)
	if err != nil {
		return nil, Peer{}, err
	}
	if err := write(ctx, c, msgHello, sealed); err != nil {
		return nil, Peer{}, err
	}
	sealedWelcome, err := readKind(ctx, c, msgWelcome)
	if err != nil {
		return nil, Peer{}, ErrWrongCode
	}
	plain, err := box.Open(key, sealedWelcome)
	if err != nil {
		return nil, Peer{}, ErrWrongCode
	}
	var w welcome
	if err := json.Unmarshal(plain, &w); err != nil || len(w.FerryKey) != box.KeySize {
		return nil, Peer{}, errors.New("pairing: malformed welcome")
	}
	return w.FerryKey, w.Host, nil
}
