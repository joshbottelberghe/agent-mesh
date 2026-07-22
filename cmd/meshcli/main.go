// meshcli — a minimal MCP client for agent-mesh nodes. Connects to a node's
// /mcp endpoint (optionally with a bearer token), initializes, and runs a tool.
// Useful for smoke-testing a node and as a harness for security checks.
//
//	meshcli -url https://host/mcp -token <tok> info
//	meshcli -url https://host/mcp -token <tok> tools
//	meshcli -url https://host/mcp -token <tok> search "partial compute"
//	meshcli -url https://host/mcp -token <tok> tasks active
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	url := flag.String("url", "", "node MCP URL, e.g. https://host/mcp")
	token := flag.String("token", "", "bearer token (optional)")
	flag.Parse()
	if *url == "" {
		usage()
	}

	headers := map[string]string{"ngrok-skip-browser-warning": "1"}
	if *token != "" {
		headers["Authorization"] = "Bearer " + *token
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, err := client.NewStreamableHttpClient(*url, transport.WithHTTPHeaders(headers))
	must(err)
	defer c.Close()
	must(c.Start(ctx))

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "meshcli", Version: "0.1.0"}
	ir, err := c.Initialize(ctx, initReq)
	must(err)
	fmt.Printf("connected: %s %s\n\n", ir.ServerInfo.Name, ir.ServerInfo.Version)

	args := flag.Args()
	cmd := "info"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "tools":
		tr, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		must(err)
		for _, t := range tr.Tools {
			fmt.Printf("- %s: %s\n", t.Name, t.Description)
		}
	case "info":
		call(ctx, c, "node_info", map[string]any{})
	case "tasks":
		a := map[string]any{}
		if len(args) > 1 {
			a["status"] = args[1]
		}
		call(ctx, c, "list_tasks", a)
	case "search":
		if len(args) < 2 {
			usage()
		}
		call(ctx, c, "search_notes", map[string]any{"query": strings.Join(args[1:], " ")})
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
	}
}

func call(ctx context.Context, c *client.Client, tool string, arguments map[string]any) {
	var req mcp.CallToolRequest
	req.Params.Name = tool
	req.Params.Arguments = arguments
	res, err := c.CallTool(ctx, req)
	must(err)
	if res.IsError {
		fmt.Fprint(os.Stderr, "tool error: ")
	}
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			fmt.Println(tc.Text)
		}
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: meshcli -url <mcp-url> [-token <tok>] <command> [args]")
	fmt.Fprintln(os.Stderr, "commands: info (default) | tools | tasks [status] | search <query>")
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
