// Command clipferry-mcp serves the Clipferry clipboard history to AI tools
// over MCP's stdio transport, for clients that start MCP servers as local
// processes. It reads the history the Clipferry app keeps on this computer;
// the app itself also serves MCP over HTTP while it runs.
//
// Usage in an MCP client configuration:
//
//	{"command": "clipferry-mcp"}
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/bkrdmrcioglu/clipferry/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	dataDir := flag.String("data-dir", core.DataDir(), "directory holding the Clipferry app's clipferry.db")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println(core.Version)
		return
	}
	log.SetOutput(os.Stderr) // stdout carries the MCP protocol

	h, err := core.OpenHistory(*dataDir)
	if err != nil {
		log.Fatalf("clipferry-mcp: %v", err)
	}
	defer h.Close()
	if !h.Installed {
		log.Printf("clipferry-mcp: no Clipferry history in %s yet; install the app from https://github.com/bkrdmrcioglu/clipferry", *dataDir)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := core.NewMCPServer(h).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatalf("clipferry-mcp: %v", err)
	}
}
