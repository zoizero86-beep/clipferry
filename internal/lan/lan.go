// Package lan finds other Clipferry devices on the local network with
// multicast DNS (Bonjour / zeroconf), so devices on the same Wi-Fi can pair
// and sync directly, without any server.
package lan

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

// Service types advertised on the network.
const (
	ServiceFerry = "_clipferry._tcp" // a paired device, reachable for sync
	ServicePair  = "_clipferry-pair._tcp"
)

// Entry is a discovered service instance.
type Entry struct {
	Instance string
	Text     map[string]string
	Addrs    []string // "ip:port", most likely reachable first
}

// Discovery advertises this device and finds others.
type Discovery interface {
	// Advertise announces a service until stop is called.
	Advertise(service, instance string, port int, text map[string]string) (stop func(), err error)
	// Browse calls found for every instance of service seen, until ctx ends.
	Browse(ctx context.Context, service string, found func(Entry)) error
}

func encodeText(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func decodeText(txt []string) map[string]string {
	m := map[string]string{}
	for _, kv := range txt {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// MDNS is the real, multicast-DNS based discovery.
type MDNS struct{}

// Advertise implements Discovery.
func (MDNS) Advertise(service, instance string, port int, text map[string]string) (func(), error) {
	srv, err := zeroconf.Register(instance, service, "local.", port, encodeText(text), nil, zeroconf.TTL(120))
	if err != nil {
		return nil, err
	}
	return srv.Shutdown, nil
}

// Browse implements Discovery. It keeps browsing until ctx ends, restarting
// the underlying query if it stops early (e.g. after a network change).
func (MDNS) Browse(ctx context.Context, service string, found func(Entry)) error {
	for ctx.Err() == nil {
		entries := make(chan *zeroconf.ServiceEntry, 16)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for e := range entries {
				found(fromZeroconf(e))
			}
		}()
		bctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := zeroconf.Browse(bctx, service, "local.", entries)
		<-bctx.Done()
		cancel()
		<-done
		if err != nil && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	return nil
}

func fromZeroconf(e *zeroconf.ServiceEntry) Entry {
	var addrs []string
	for _, ip := range e.AddrIPv4 {
		if ip.IsLoopback() {
			continue
		}
		addrs = append(addrs, net.JoinHostPort(ip.String(), strconv.Itoa(e.Port)))
	}
	for _, ip := range e.AddrIPv6 {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue // link-local addresses need a zone we don't get here
		}
		addrs = append(addrs, net.JoinHostPort(ip.String(), strconv.Itoa(e.Port)))
	}
	if len(addrs) == 0 {
		for _, ip := range e.AddrIPv4 { // same machine, e.g. two test instances
			addrs = append(addrs, net.JoinHostPort(ip.String(), strconv.Itoa(e.Port)))
		}
	}
	return Entry{Instance: e.Instance, Text: decodeText(e.Text), Addrs: addrs}
}

// Memory is an in-process Discovery for tests and the demo: everything
// advertised on it is reachable at 127.0.0.1.
type Memory struct {
	mu      sync.Mutex
	entries map[string]map[string]Entry // service → instance → entry
}

// NewMemory returns an empty in-process network.
func NewMemory() *Memory { return &Memory{entries: map[string]map[string]Entry{}} }

// Advertise implements Discovery.
func (m *Memory) Advertise(service, instance string, port int, text map[string]string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries[service] == nil {
		m.entries[service] = map[string]Entry{}
	}
	t := map[string]string{}
	for k, v := range text {
		t[k] = v
	}
	m.entries[service][instance] = Entry{Instance: instance, Text: t, Addrs: []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.entries[service], instance)
	}, nil
}

// Browse implements Discovery.
func (m *Memory) Browse(ctx context.Context, service string, found func(Entry)) error {
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		m.mu.Lock()
		var list []Entry
		for _, e := range m.entries[service] {
			list = append(list, e)
		}
		m.mu.Unlock()
		for _, e := range list {
			found(e)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
