// Package relay is the Clipferry relay: a dumb WebSocket fan-out between the
// devices of a ferry. Every frame it handles is end-to-end encrypted by the
// devices, so the relay sees only opaque bytes, room names and timing.
//
// Rooms are named by the clients. A frame sent into a room is forwarded to
// every other connection in it and kept briefly so a device that was offline
// (laptop lid closed, network change) still receives the latest clips.
package relay

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Options configures a relay. Zero values pick sensible defaults.
type Options struct {
	// Token, when set, must be presented as "Authorization: Bearer <token>"
	// or "?token=<token>". Leave empty for a public relay.
	Token string
	// MaxFrame is the largest frame accepted, in bytes.
	MaxFrame int64
	// Backlog is how many recent frames a ferry room keeps for late joiners.
	Backlog int
	// BacklogTTL is how long a kept frame stays replayable.
	BacklogTTL time.Duration
	// MaxPeers caps connections per room.
	MaxPeers int
	// Logf receives one line per notable event; nil silences the relay.
	Logf func(format string, args ...any)
}

type frame struct {
	data []byte
	at   time.Time
}

type peer struct {
	conn *websocket.Conn
	send chan []byte
}

type room struct {
	peers   map[*peer]struct{}
	backlog []frame
}

// Server is an http.Handler serving /v1/rooms/{room} and /healthz.
type Server struct {
	opts  Options
	mu    sync.Mutex
	rooms map[string]*room
	mux   *http.ServeMux
}

var roomName = regexp.MustCompile(`^(?:[a-f0-9]{32}|pair-[0-9]{3})$`)

// New returns a relay server.
func New(opts Options) *Server {
	if opts.MaxFrame == 0 {
		opts.MaxFrame = 2 << 20
	}
	if opts.Backlog == 0 {
		opts.Backlog = 20
	}
	if opts.BacklogTTL == 0 {
		opts.BacklogTTL = 24 * time.Hour
	}
	if opts.MaxPeers == 0 {
		opts.MaxPeers = 16
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	s := &Server{opts: opts, rooms: map[string]*room{}, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/rooms/{room}", s.handleRoom)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("Clipferry relay. It forwards end-to-end encrypted clips between paired devices and cannot read them.\nhttps://github.com/bkrdmrcioglu/clipferry\n"))
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Run starts the janitor that forgets expired backlog and empty rooms. When
// ctx ends it disconnects everyone: hijacked WebSocket connections are not
// closed by http.Server.Close.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for _, rm := range s.rooms {
				for p := range rm.peers {
					p.conn.Close(websocket.StatusGoingAway, "relay shutting down")
				}
			}
			s.mu.Unlock()
			return
		case <-t.C:
			s.sweep(time.Now())
		}
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.opts.Token == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) == 1
}

// ttl is shorter for pairing rooms: a pairing code is only valid for minutes.
func (s *Server) ttl(name string) time.Duration {
	if strings.HasPrefix(name, "pair-") {
		return 10 * time.Minute
	}
	return s.opts.BacklogTTL
}

func (s *Server) handleRoom(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("room")
	if !roomName.MatchString(name) {
		http.Error(w, "invalid room", http.StatusBadRequest)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	pairing := strings.HasPrefix(name, "pair-")
	maxPeers := s.opts.MaxPeers
	if pairing {
		maxPeers = 2
	}

	s.mu.Lock()
	rm := s.rooms[name]
	if rm == nil {
		rm = &room{peers: map[*peer]struct{}{}}
		s.rooms[name] = rm
	}
	full := len(rm.peers) >= maxPeers
	s.mu.Unlock()
	if full {
		http.Error(w, "room is full", http.StatusConflict)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(s.opts.MaxFrame)
	p := &peer{conn: conn, send: make(chan []byte, 64)}

	s.mu.Lock()
	// "fresh" lets the device that opens a pairing session discard stale
	// frames from an earlier attempt that used the same pairing channel.
	if r.URL.Query().Get("fresh") == "1" {
		rm.backlog = nil
	}
	now := time.Now()
	for _, f := range rm.backlog {
		if now.Sub(f.at) < s.ttl(name) {
			select {
			case p.send <- f.data:
			default:
			}
		}
	}
	rm.peers[p] = struct{}{}
	peers := len(rm.peers)
	s.mu.Unlock()
	s.opts.Logf("join room=%s peers=%d", shortName(name), peers)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.writeLoop(ctx, p)

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		s.broadcast(name, rm, p, data)
	}

	s.mu.Lock()
	delete(rm.peers, p)
	peers = len(rm.peers)
	s.mu.Unlock()
	conn.CloseNow()
	s.opts.Logf("leave room=%s peers=%d", shortName(name), peers)
}

func (s *Server) writeLoop(ctx context.Context, p *peer) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-p.send:
			wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := p.conn.Write(wctx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				p.conn.CloseNow()
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := p.conn.Ping(pctx)
			cancel()
			if err != nil {
				p.conn.CloseNow()
				return
			}
		}
	}
}

func (s *Server) broadcast(name string, rm *room, from *peer, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm.backlog = append(rm.backlog, frame{data: data, at: time.Now()})
	if len(rm.backlog) > s.opts.Backlog {
		rm.backlog = rm.backlog[len(rm.backlog)-s.opts.Backlog:]
	}
	for p := range rm.peers {
		if p == from {
			continue
		}
		select {
		case p.send <- data:
		default:
			// A peer that cannot keep up is dropped; it will reconnect and
			// catch up from the backlog.
			go p.conn.Close(websocket.StatusTryAgainLater, "slow consumer")
		}
	}
}

func (s *Server) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, rm := range s.rooms {
		kept := rm.backlog[:0]
		for _, f := range rm.backlog {
			if now.Sub(f.at) < s.ttl(name) {
				kept = append(kept, f)
			}
		}
		rm.backlog = kept
		if len(rm.peers) == 0 && len(rm.backlog) == 0 {
			delete(s.rooms, name)
		}
	}
}

func shortName(name string) string {
	if len(name) > 8 && !strings.HasPrefix(name, "pair-") {
		return name[:8] + "…"
	}
	return name
}

// LogStd is a convenience Logf that writes to the standard logger.
func LogStd(format string, args ...any) { log.Printf(format, args...) }
