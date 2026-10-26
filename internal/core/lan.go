package core

// Same-network mode: no server anywhere. Every device runs a tiny relay of
// its own on the local network and announces itself with mDNS. Among the
// devices of a ferry that can see each other, the one with the smallest id
// is the meeting point; the others connect to it, and it connects to itself.
// When that device goes away the others fall back to their own relay until
// it, or another device, shows up again. Frames are end-to-end encrypted
// exactly as over a relay server.

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/box"
	"github.com/bkrdmrcioglu/clipferry/internal/ferry"
	"github.com/bkrdmrcioglu/clipferry/internal/lan"
	"github.com/bkrdmrcioglu/clipferry/internal/relay"
)

// DefaultLANPort is the port the same-network relay listens on, so firewall
// rules stay valid across restarts. Another port is used if it is taken.
const DefaultLANPort = 47824

// peerStale is how long a device announced on the network is considered
// present without being heard from again.
const peerStale = 15 * time.Minute

type lanPeer struct {
	tag   string
	addrs []string
	seen  time.Time
}

type lanState struct {
	ln   net.Listener
	port int

	mu         sync.Mutex
	peers      map[string]lanPeer
	advertTag  string
	advertStop func()
	target     string // URL the sync connection currently uses
}

// lanMode reports whether this device syncs over the local network, which is
// the case unless the user configured a relay server.
func (e *Engine) lanMode() bool {
	return e.lan != nil && e.Settings().RelayURL == ""
}

func lanTag(key []byte) string { return hex.EncodeToString(box.Derive(key, "lan")[:8]) }

// openLAN opens the same-network relay's listener. Called from New, so that
// e.lan never changes once the engine is shared.
func (e *Engine) openLAN() error {
	if e.opts.Discovery == nil {
		return nil
	}
	addr := e.opts.LANListen
	if addr == "" {
		addr = net.JoinHostPort("", itoa(DefaultLANPort))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		host, _, _ := net.SplitHostPort(addr)
		if ln, err = net.Listen("tcp", net.JoinHostPort(host, "0")); err != nil {
			return err
		}
	}
	e.lan = &lanState{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, peers: map[string]lanPeer{}}
	return nil
}

// startLAN serves the same-network relay and starts looking for devices.
func (e *Engine) startLAN(ctx context.Context) {
	if e.lan == nil {
		return
	}
	rs := relay.New(relay.Options{Backlog: 50})
	go rs.Run(ctx)
	srv := &http.Server{Handler: rs, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(e.lan.ln)
	go e.opts.Discovery.Browse(ctx, lan.ServiceFerry, e.onLANEntry)
	go func() {
		<-ctx.Done()
		srv.Close()
		e.lan.mu.Lock()
		if e.lan.advertStop != nil {
			e.lan.advertStop()
			e.lan.advertStop, e.lan.advertTag = nil, ""
		}
		e.lan.mu.Unlock()
	}()
}

// LANPort returns the port of the same-network relay, or 0.
func (e *Engine) LANPort() int {
	if e.lan == nil {
		return 0
	}
	return e.lan.port
}

func (e *Engine) loopbackURL() string { return "ws://127.0.0.1:" + itoa(e.lan.port) }

func (e *Engine) onLANEntry(en lan.Entry) {
	id, tag := en.Text["id"], en.Text["f"]
	if id == "" || id == e.Self().ID || len(en.Addrs) == 0 {
		return
	}
	l := e.lan
	l.mu.Lock()
	prev, known := l.peers[id]
	l.peers[id] = lanPeer{tag: tag, addrs: en.Addrs, seen: time.Now()}
	changed := !known || prev.tag != tag || !slices.Equal(prev.addrs, en.Addrs)
	l.mu.Unlock()
	if changed && e.lanMode() {
		e.retarget()
	}
}

// lanTarget picks the meeting point for key: the smallest device id among
// this device and the ferry's devices currently seen on the network.
func (e *Engine) lanTarget(key []byte) (url, peerID string) {
	tag := lanTag(key)
	self := e.Self().ID
	leader, addr := self, ""
	l := e.lan
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, p := range l.peers {
		if p.tag == tag && time.Since(p.seen) < peerStale && id < leader {
			leader, addr = id, p.addrs[0]
		}
	}
	if leader == self {
		return e.loopbackURL(), ""
	}
	return "ws://" + addr, leader
}

// retarget reconnects if the meeting point changed.
func (e *Engine) retarget() {
	e.mu.Lock()
	key := e.ferryKey
	e.mu.Unlock()
	if key == nil {
		return
	}
	url, _ := e.lanTarget(key)
	e.lan.mu.Lock()
	same := url == e.lan.target
	e.lan.mu.Unlock()
	if !same {
		e.reconnect()
	}
}

// forgetPeer drops a device we could not reach, so the next attempt picks
// another meeting point; mDNS brings it back when it is reachable again.
func (e *Engine) forgetPeer(id string) {
	e.lan.mu.Lock()
	delete(e.lan.peers, id)
	e.lan.mu.Unlock()
}

// updateAdvert announces this device as a member of the ferry (or stops).
func (e *Engine) updateAdvert(key []byte) {
	if e.lan == nil {
		return
	}
	want := ""
	if key != nil && e.lanMode() {
		want = lanTag(key)
	}
	l := e.lan
	l.mu.Lock()
	defer l.mu.Unlock()
	if want == l.advertTag {
		return
	}
	if l.advertStop != nil {
		l.advertStop()
		l.advertStop = nil
	}
	l.advertTag = ""
	if want == "" {
		return
	}
	self := e.Self()
	stop, err := e.opts.Discovery.Advertise(lan.ServiceFerry, self.ID, l.port, map[string]string{"id": self.ID, "f": want})
	if err != nil {
		e.opts.Logf("mdns advertise: %v", err)
		return
	}
	l.advertStop, l.advertTag = stop, want
}

// dialTarget returns where the sync connection should go now. peerID is set
// when it is another device on the network.
func (e *Engine) dialTarget(key []byte) (ferry.Relay, string) {
	if !e.lanMode() {
		return e.relay(), ""
	}
	url, peer := e.lanTarget(key)
	e.lan.mu.Lock()
	e.lan.target = url
	e.lan.mu.Unlock()
	return ferry.Relay{URL: url}, peer
}

// advertisePairing makes a pairing code findable on the network.
func (e *Engine) advertisePairing(code string) func() {
	if !e.lanMode() {
		return func() {}
	}
	digits, _ := ferry.NormalizeCode(code)
	self := e.Self()
	stop, err := e.opts.Discovery.Advertise(lan.ServicePair, "pair-"+digits[:3]+"-"+self.ID[:6], e.lan.port, map[string]string{"ch": digits[:3]})
	if err != nil {
		e.opts.Logf("mdns advertise pairing: %v", err)
		return func() {}
	}
	return stop
}

// ErrNoPairHost means no device on the network is showing the code.
var ErrNoPairHost = errors.New("couldn't find the other computer on this network — check that both are on the same Wi-Fi, that the code is still shown, and that Clipferry is allowed through the firewall")

// findPairHost looks for the device showing a code on the network.
func (e *Engine) findPairHost(ctx context.Context, code string) (ferry.Relay, error) {
	digits, _ := ferry.NormalizeCode(code)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	found := make(chan string, 1)
	go e.opts.Discovery.Browse(ctx, lan.ServicePair, func(en lan.Entry) {
		if en.Text["ch"] == digits[:3] && len(en.Addrs) > 0 {
			select {
			case found <- en.Addrs[0]:
			default:
			}
		}
	})
	select {
	case addr := <-found:
		return ferry.Relay{URL: "ws://" + addr}, nil
	case <-ctx.Done():
		return ferry.Relay{}, ErrNoPairHost
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
