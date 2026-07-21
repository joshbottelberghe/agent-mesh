package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerTools wires this node's tools. Every tool reads only from paths the
// operator put in config — the code is generic, the data is private and local.
func registerTools(s *server.MCPServer, cfg *Config) {
	s.AddTool(
		mcp.NewTool("node_info",
			mcp.WithDescription("Identify this agent-mesh node and list what it exposes. Use this first when peering to discover a node's capabilities."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			tasks := countMarkdown(cfg.Data.TasksDir)
			notes := 0
			for _, d := range cfg.Data.NotesDirs {
				notes += countMarkdown(d)
			}
			out := fmt.Sprintf("node: %s\ntools: node_info, list_tasks, search_notes\ntasks indexed: %d\nnotes indexed: %d",
				cfg.NodeName, tasks, notes)
			return mcp.NewToolResultText(out), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_tasks",
			mcp.WithDescription("List this node's tasks (title, status, priority), optionally filtered by status."),
			mcp.WithString("status", mcp.Description("optional filter: active | blocked | completed")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			want := strings.ToLower(req.GetString("status", ""))
			var rows []string
			walkMarkdown(cfg.Data.TasksDir, func(path string, body string) {
				fm := frontmatter(body)
				st := fm["status"]
				if want != "" && strings.ToLower(st) != want {
					return
				}
				title := fm["title"]
				if title == "" {
					title = filepath.Base(path)
				}
				rows = append(rows, fmt.Sprintf("- [%s] %s (priority: %s)", orDash(st), title, orDash(fm["priority"])))
			})
			if len(rows) == 0 {
				return mcp.NewToolResultText("no matching tasks"), nil
			}
			sort.Strings(rows)
			return mcp.NewToolResultText(strings.Join(rows, "\n")), nil
		},
	)

	s.AddTool(
		mcp.NewTool("search_notes",
			mcp.WithDescription("Full-text search this node's notes/memory. Returns matching file and a snippet."),
			mcp.WithString("query", mcp.Required(), mcp.Description("text to search for (case-insensitive)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError("query is required"), nil
			}
			ql := strings.ToLower(q)
			var hits []string
			for _, dir := range cfg.Data.NotesDirs {
				walkMarkdown(dir, func(path string, body string) {
					if len(hits) >= 20 {
						return
					}
					for _, line := range strings.Split(body, "\n") {
						if strings.Contains(strings.ToLower(line), ql) {
							hits = append(hits, fmt.Sprintf("%s: %s", filepath.Base(path), strings.TrimSpace(line)))
							break
						}
					}
				})
			}
			if len(hits) == 0 {
				return mcp.NewToolResultText("no matches"), nil
			}
			return mcp.NewToolResultText(strings.Join(hits, "\n")), nil
		},
	)
}

// --- small markdown helpers (no deps) ---

func walkMarkdown(dir string, fn func(path, body string)) {
	if dir == "" {
		return
	}
	filepath.WalkDir(expand(dir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil {
			fn(path, string(b))
		}
		return nil
	})
}

func countMarkdown(dir string) int {
	n := 0
	walkMarkdown(dir, func(string, string) { n++ })
	return n
}

// frontmatter pulls simple key: value pairs from a leading --- ... --- block.
func frontmatter(body string) map[string]string {
	m := map[string]string{}
	if !strings.HasPrefix(body, "---") {
		return m
	}
	end := strings.Index(body[3:], "\n---")
	if end < 0 {
		return m
	}
	for _, line := range strings.Split(body[3:3+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
