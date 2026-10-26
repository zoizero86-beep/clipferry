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

// mcpHandler exposes the clipboard history to AI tools over Streamable HTTP.
func (e *Engine) mcpHandler() http.Handler {
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
			q.ExcludeDev = e.Self().ID
		}
		if a.Device != "" {
			id, err := e.deviceByName(a.Device)
			if err != nil {
				return textResult(err.Error()), nil, nil
			}
			q.DeviceID = id
		}
		clips, err := e.st.ListClips(q)
		if err != nil {
			return nil, nil, err
		}
		if len(clips) == 0 {
			return textResult("The clipboard history is empty."), nil, nil
		}
		return textResult(e.formatClip(clips[0], true)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_recent_clips",
		Description: "List recent clipboard history across the user's devices, newest first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a recentArgs) (*mcp.CallToolResult, any, error) {
		q := store.Query{Limit: clampLimit(a.Limit)}
		if a.Device != "" {
			id, err := e.deviceByName(a.Device)
			if err != nil {
				return textResult(err.Error()), nil, nil
			}
			q.DeviceID = id
		}
		clips, err := e.st.ListClips(q)
		if err != nil {
			return nil, nil, err
		}
		return textResult(e.formatClips(clips, "The clipboard history is empty.")), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_clips",
		Description: "Search the clipboard history of all the user's devices for text.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Query) == "" {
			return textResult("query is required"), nil, nil
		}
		clips, err := e.st.ListClips(store.Query{Text: a.Query, Limit: clampLimit(a.Limit)})
		if err != nil {
			return nil, nil, err
		}
		return textResult(e.formatClips(clips, fmt.Sprintf("No clips match %q.", a.Query))), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices that share this clipboard.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		ds, err := e.st.Devices()
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

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func clampLimit(n int) int {
	if n <= 0 {
		return 10
	}
	return min(n, 50)
}

func (e *Engine) deviceByName(name string) (string, error) {
	ds, err := e.st.Devices()
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

func (e *Engine) formatClip(c store.Clip, full bool) string {
	content := c.Content
	if c.Sensitive && !e.Settings().MCPShowSecrets {
		content = "[withheld: " + strings.ToLower(secrets.LabelFor(c.SecretKind)) +
			". The user can paste it directly, or allow AI tools to read secrets in Clipferry settings.]"
	} else if !full && len(content) > 2000 {
		content = content[:2000] + "… [truncated]"
	}
	return fmt.Sprintf("From %s (%s) at %s, id %s:\n%s", c.DeviceName, c.Platform, c.CreatedAt.Format(time.RFC3339), c.ID, content)
}

func (e *Engine) formatClips(clips []store.Clip, empty string) string {
	if len(clips) == 0 {
		return empty
	}
	parts := make([]string, len(clips))
	for i, c := range clips {
		parts[i] = e.formatClip(c, false)
	}
	return strings.Join(parts, "\n\n---\n\n")
}
