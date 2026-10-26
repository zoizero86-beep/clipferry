// Command clipferry-relay runs a Clipferry relay. Devices of a ferry meet here
// when they cannot reach each other directly; every frame is end-to-end
// encrypted, so the relay cannot read clips.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/relay"
)

func main() {
	addr := ":" + envOr("PORT", "8080")
	srv := relay.New(relay.Options{
		Token: os.Getenv("CLIPFERRY_RELAY_TOKEN"),
		Logf:  relay.LogStd,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Run(ctx)

	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()

	mode := "public (no token)"
	if os.Getenv("CLIPFERRY_RELAY_TOKEN") != "" {
		mode = "private (token required)"
	}
	log.Printf("clipferry-relay listening on %s, %s", addr, mode)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
