// Command clipferry-demo runs two simulated devices in one process, on a
// simulated local network, so the UI, pairing and sync can be tried on a
// single machine. Clipboards are simulated; to "copy" on a device, POST
// {"content": "..."} to its /api/clips.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/core"
	"github.com/bkrdmrcioglu/clipferry/internal/lan"
	"github.com/bkrdmrcioglu/clipferry/internal/store"
)

type memClipboard struct {
	mu   sync.Mutex
	text string
}

func (m *memClipboard) Text() (string, error) { m.mu.Lock(); defer m.mu.Unlock(); return m.text, nil }
func (m *memClipboard) SetText(s string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text = s
	return nil
}

// memAutostart stands in for the OS start-at-login setting.
type memAutostart struct {
	mu sync.Mutex
	on bool
}

func (m *memAutostart) IsEnabled() (bool, error) { m.mu.Lock(); defer m.mu.Unlock(); return m.on, nil }
func (m *memAutostart) Enable() error            { m.mu.Lock(); defer m.mu.Unlock(); m.on = true; return nil }
func (m *memAutostart) Disable() error           { m.mu.Lock(); defer m.mu.Unlock(); m.on = false; return nil }

func main() {
	basePort := flag.Int("port", 47901, "port of the first device; the second uses port+1")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	network := lan.NewMemory()

	type dev struct {
		name, platform string
		e              *core.Engine
		cb             *memClipboard
	}
	devs := []*dev{{name: "MacBook Pro", platform: "macos"}, {name: "Office PC", platform: "windows"}}
	for i, d := range devs {
		st, err := store.Open(":memory:")
		if err != nil {
			log.Fatal(err)
		}
		d.cb = &memClipboard{}
		d.e, err = core.New(core.Options{Store: st, Clipboard: d.cb, DeviceName: d.name, Platform: d.platform,
			Discovery: network, LANListen: "127.0.0.1:0"})
		if err != nil {
			log.Fatal(err)
		}
		d.e.SetAutostart(&memAutostart{})
		go d.e.Run(ctx)
		ln, err := core.Listen(*basePort + i)
		if err != nil {
			log.Fatal(err)
		}
		go d.e.Serve(ctx, ln)
	}
	time.Sleep(100 * time.Millisecond)

	// Pair the two devices like a user would.
	ps, err := devs[0].e.StartPairing()
	if err != nil {
		log.Fatal(err)
	}
	if _, err := devs[1].e.JoinPairing(ps.Code); err != nil {
		log.Fatal(err)
	}
	for !(devs[0].e.Status().Connected && devs[1].e.Status().Connected) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	seed := []struct {
		dev  int
		text string
	}{
		{0, "docker compose up -d --build"},
		{1, "Toplantı notları: yeni web sitesi için logo revizyonu cuma gününe kadar bitmeli. Müşteri koyu tema da istiyor, mobilde menü sağdan açılsın."},
		{0, "https://github.com/bkrdmrcioglu/clipferry"},
		{1, "SELECT id, email, created_at\nFROM users\nWHERE created_at > now() - interval '7 days'\nORDER BY created_at DESC;"},
		{0, "ghp_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"},
		{1, "func main() {\n\tfmt.Println(\"hello from the office\")\n}"},
		{0, "ssh deploy@203.0.113.10 -p 2222"},
	}
	for _, s := range seed {
		devs[s.dev].cb.SetText(s.text)
		time.Sleep(600 * time.Millisecond)
	}

	for i, d := range devs {
		fmt.Printf("%-12s http://127.0.0.1:%d/#token=%s\n", d.name, *basePort+i, d.e.Token())
	}
	fmt.Println("Ctrl+C to stop.")
	<-ctx.Done()
}
