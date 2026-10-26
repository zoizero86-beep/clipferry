package core

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/store"
	"github.com/bkrdmrcioglu/clipferry/internal/ui"
)

// DefaultPort is where the local API, the app window and MCP are served.
const DefaultPort = 47823

// Handler serves the app UI, the local REST API used by it, and the MCP
// endpoint. Everything except the static UI requires the local token.
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()

	api.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		devices, _ := e.st.Devices()
		count, _ := e.st.Count()
		writeJSON(w, http.StatusOK, map[string]any{
			"version":   Version,
			"self":      e.Self(),
			"status":    e.Status(),
			"settings":  e.Settings(),
			"devices":   devices,
			"pairing":   e.Pairing(),
			"autostart": e.Autostart(), // null when not controllable
			"count":     count,
			"mcp_url":   "http://" + r.Host + "/mcp",
		})
	})

	api.HandleFunc("GET /api/clips", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		clips, err := e.st.ListClips(store.Query{
			Text:       r.URL.Query().Get("q"),
			DeviceID:   r.URL.Query().Get("device"),
			PinnedOnly: r.URL.Query().Get("pinned") == "1",
			Limit:      limit,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, clips)
	})

	api.HandleFunc("POST /api/clips/{id}/copy", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, e.Copy(r.PathValue("id")))
	})
	api.HandleFunc("POST /api/clips/{id}/pin", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Pinned bool }
		if !readJSON(w, r, &body) {
			return
		}
		writeResult(w, e.Pin(r.PathValue("id"), body.Pinned))
	})
	api.HandleFunc("DELETE /api/clips/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, e.Delete(r.PathValue("id")))
	})
	api.HandleFunc("DELETE /api/clips", func(w http.ResponseWriter, r *http.Request) {
		n, err := e.Clear()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
	})
	// Lets the UI (and tests) add a clip as if it had been copied here.
	api.HandleFunc("POST /api/clips", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Content string }
		if !readJSON(w, r, &body) {
			return
		}
		c, err := e.Capture(body.Content)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"clip": c})
	})

	api.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		cur := e.Settings()
		if !readJSON(w, r, &cur) {
			return
		}
		s, err := e.UpdateSettings(cur)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	api.HandleFunc("PUT /api/autostart", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Enabled bool }
		if !readJSON(w, r, &body) {
			return
		}
		if err := e.SetAutostartEnabled(body.Enabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
	})

	api.HandleFunc("POST /api/pair/start", func(w http.ResponseWriter, r *http.Request) {
		ps, err := e.StartPairing()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ps)
	})
	api.HandleFunc("POST /api/pair/join", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Code string }
		if !readJSON(w, r, &body) {
			return
		}
		ps, err := e.JoinPairing(body.Code)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ps)
	})
	api.HandleFunc("POST /api/pair/cancel", func(w http.ResponseWriter, r *http.Request) {
		e.CancelPairing()
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("POST /api/leave", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, e.Leave())
	})

	api.HandleFunc("GET /api/events", e.serveEvents)

	mux.Handle("/api/", e.requireToken(api))
	mux.Handle("/mcp", e.requireToken(e.mcpHandler()))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	static, _ := fs.Sub(ui.FS, "dist")
	if dir := os.Getenv("CLIPFERRY_UI_DIR"); dir != "" {
		static = os.DirFS(dir) // live-edit the UI during development
	}
	mux.Handle("/", noCache(http.FileServerFS(static)))
	return mux
}

// serveEvents streams engine events as Server-Sent Events.
func (e *Engine) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch, stop := e.Subscribe()
	defer stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (e *Engine) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token") // EventSource cannot set headers
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(e.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// localOnly rejects requests whose Host is not a loopback name, which stops
// DNS-rebinding attacks from web pages open in a browser on this machine.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// Listen binds the local server to 127.0.0.1:port. Port 0 picks a free port.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// Serve runs the local server on ln until ctx is cancelled.
func (e *Engine) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: localOnly(e.Handler()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxClipBytes+4096)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
