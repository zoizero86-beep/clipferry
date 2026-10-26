package core

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/secrets"
	"github.com/bkrdmrcioglu/clipferry/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type latestArgs struct {
	FromOtherDevice bool   `json:"from_other_device,omitempty" jsonschema:"Only consider clips copied on another device of the ferry"`
	Device          string `json:"device,omitempty" jsonschema:"Only consider clips from the device with this name"`
}

type recentArgs struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"How many clips to return, 1-50 (default 10)"`
	Device string `json:"device,omitempty" jsonschema:"Only clips from the device with this name"`
}

type searchArgs struct {
	Query string `json:"query" jsonschema:"Text to look for in the clipboard history"`
	Limit int    `json:"limit,omitempty" jsonschema:"How many clips to return, 1-50 (default 10)"`
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// MCPSource is what the MCP tools read: the clipboard history and two
// facts about this device. The running engine is one; a read-only view of
// the app's database (see OpenHistory) is another.
type MCPSource interface {
	Store() *store.Store
	SelfID() string
	ShowSecrets() bool
}

// SelfID is this device's id.
func (e *Engine) SelfID() string { return e.Self().ID }

// ShowSecrets reports whether AI tools may read sensitive clips.
func (e *Engine) ShowSecrets() bool { return e.Settings().MCPShowSecrets }

// mcpHandler exposes the clipboard history to AI tools over Streamable HTTP.
func (e *Engine) mcpHandler() http.Handler {
	server := NewMCPServer(e)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// NewMCPServer builds the Clipferry MCP server over src.
func NewMCPServer(src MCPSource) *mcp.Server {
	t := mcpTools{src}
	server := mcp.NewServer(&mcp.Implementation{Name: "clipferry", Version: Version}, &mcp.ServerOptions{
		Instructions: "Clipferry is the user's shared clipboard across their computers. Use it when the user refers to " +
			"something they just copied, especially on another machine. Sensitive clips (passwords, keys) are " +
			"withheld unless the user enabled sharing them with AI tools.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_latest_clip",
		Description: "Get the most recently copied text, optionally only from another device (\"what I just copied on my other machine\").",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a latestArgs) (*mcp.CallToolResult, any, error) {
		q := store.Query{Limit: 1}
		if a.FromOtherDevice {
			q.ExcludeDev = t.src.SelfID()
		}
		if a.Device != "" {
			id, err := t.deviceByName(a.Device)
			if err != nil {
				return textResult(err.Error()), nil, nil
			}
			q.DeviceID = id
		}
		clips, err := t.src.Store().ListClips(q)
		if err != nil {
			return nil, nil, err
		}
		if len(clips) == 0 {
			return textResult("The clipboard history is empty."), nil, nil
		}
		return textResult(t.formatClip(clips[0], true)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_recent_clips",
		Description: "List recent clipboard history across the user's devices, newest first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a recentArgs) (*mcp.CallToolResult, any, error) {
		q := store.Query{Limit: clampLimit(a.Limit)}
		if a.Device != "" {
			id, err := t.deviceByName(a.Device)
			if err != nil {
				return textResult(err.Error()), nil, nil
			}
			q.DeviceID = id
		}
		clips, err := t.src.Store().ListClips(q)
		if err != nil {
			return nil, nil, err
		}
		return textResult(t.formatClips(clips, "The clipboard history is empty.")), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_clips",
		Description: "Search the clipboard history of all the user's devices for text.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Query) == "" {
			return textResult("query is required"), nil, nil
		}
		clips, err := t.src.Store().ListClips(store.Query{Text: a.Query, Limit: clampLimit(a.Limit)})
		if err != nil {
			return nil, nil, err
		}
		return textResult(t.formatClips(clips, fmt.Sprintf("No clips match %q.", a.Query))), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices that share this clipboard.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		ds, err := t.src.Store().Devices()
		if err != nil {
			return nil, nil, err
		}
		var b strings.Builder
		for _, d := range ds {
			self := ""
			if d.Self {
				self = " (this device)"
			}
			fmt.Fprintf(&b, "%s — %s%s, last seen %s\n", d.Name, d.Platform, self, d.LastSeen.Format(time.RFC3339))
		}
		return textResult(strings.TrimSpace(b.String())), nil, nil
	})

	return server
}

type mcpTools struct{ src MCPSource }

func clampLimit(n int) int {
	if n <= 0 {
		return 10
	}
	return min(n, 50)
}

func (t mcpTools) deviceByName(name string) (string, error) {
	ds, err := t.src.Store().Devices()
	if err != nil {
		return "", err
	}
	var names []string
	for _, d := range ds {
		if strings.EqualFold(d.Name, name) {
			return d.ID, nil
		}
		names = append(names, d.Name)
	}
	return "", fmt.Errorf("no device named %q; known devices: %s", name, strings.Join(names, ", "))
}

func (t mcpTools) formatClip(c store.Clip, full bool) string {
	content := c.Content
	if c.Sensitive && !t.src.ShowSecrets() {
		content = "[withheld: " + strings.ToLower(secrets.LabelFor(c.SecretKind)) +
			". The user can paste it directly, or allow AI tools to read secrets in Clipferry settings.]"
	} else if !full && len(content) > 2000 {
		content = content[:2000] + "… [truncated]"
	}
	return fmt.Sprintf("From %s (%s) at %s, id %s:\n%s", c.DeviceName, c.Platform, c.CreatedAt.Format(time.RFC3339), c.ID, content)
}

func (t mcpTools) formatClips(clips []store.Clip, empty string) string {
	if len(clips) == 0 {
		return empty
	}
	parts := make([]string, len(clips))
	for i, c := range clips {
		parts[i] = t.formatClip(c, false)
	}
	return strings.Join(parts, "\n\n---\n\n")
}
