package core

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/lan"
	"github.com/bkrdmrcioglu/clipferry/internal/store"
)

func startLANEngine(t *testing.T, ctx context.Context, st *store.Store, cb *fakeClipboard, net *lan.Memory, name string) *device {
	t.Helper()
	e, err := New(Options{Store: st, Clipboard: cb, DeviceName: name, PollInterval: 20 * time.Millisecond,
		Discovery: net, LANListen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ev, stop := e.Subscribe()
	t.Cleanup(stop)
	go e.Run(ctx)
	api := httptest.NewServer(localOnly(e.Handler()))
	t.Cleanup(api.Close)
	return &device{name: name, cb: cb, e: e, api: api, ev: ev}
}

// Two computers on the same Wi-Fi: no relay server anywhere.
func TestEndToEndSameNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	network := lan.NewMemory()

	stores := map[string]*store.Store{}
	for _, n := range []string{"mac", "pc"} {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		stores[n] = st
	}
	ctxs := map[string]context.CancelFunc{}
	start := func(n, name string, cb *fakeClipboard) *device {
		c, stop := context.WithCancel(ctx)
		ctxs[n] = stop
		return startLANEngine(t, c, stores[n], cb, network, name)
	}
	mac := start("mac", "MacBook", &fakeClipboard{})
	pc := start("pc", "Office PC", &fakeClipboard{})
	if mac.e.Status().Mode != "lan" {
		t.Fatalf("mode = %q, want lan", mac.e.Status().Mode)
	}

	ps, err := mac.e.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.e.JoinPairing(ps.Code); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, mac, "pair.done")
	waitEvent(t, pc, "pair.done")
	t.Logf("✓ paired over the local network with %s, no server", ps.Code)

	mac.cb.SetText("hello over wifi")
	waitFor(t, "mac → pc", func() bool { s, _ := pc.cb.Text(); return s == "hello over wifi" })
	pc.cb.SetText("and back again")
	waitFor(t, "pc → mac", func() bool { s, _ := mac.cb.Text(); return s == "and back again" })
	t.Log("✓ syncs both ways")

	// The device that is the meeting point goes to sleep; the other keeps
	// copying, and everything arrives once it is back.
	leaderKey, leader, follower := "mac", mac, pc
	if pc.e.Self().ID < mac.e.Self().ID {
		leaderKey, leader, follower = "pc", pc, mac
	}
	ctxs[leaderKey]()
	waitFor(t, "follower to fall back to its own relay", func() bool {
		follower.e.lan.mu.Lock()
		defer follower.e.lan.mu.Unlock()
		return follower.e.lan.target == follower.e.loopbackURL()
	})
	follower.cb.SetText("copied while the other one slept")
	time.Sleep(200 * time.Millisecond)

	back := start(leaderKey, leader.name, leader.cb)
	waitFor(t, "catch-up after the device returns", func() bool {
		cs, _ := back.e.Store().ListClips(store.Query{Text: "slept"})
		return len(cs) == 1
	})
	n := 0
	waitFor(t, "sync resumes", func() bool {
		n++
		follower.cb.SetText("after the reunion " + itoa(n))
		time.Sleep(80 * time.Millisecond)
		s, _ := back.cb.Text()
		return strings.HasPrefix(s, "after the reunion")
	})
	t.Log("✓ meeting point left and returned; missed clips caught up")
}
