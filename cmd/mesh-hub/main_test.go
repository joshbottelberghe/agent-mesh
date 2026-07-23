package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// --- unit: per-consumer allow-list ---

func TestConsumerAllows(t *testing.T) {
	cases := []struct {
		allow    []string
		upstream string
		want     bool
	}{
		{[]string{"*"}, "anything", true},
		{[]string{"josh", "board"}, "josh", true},
		{[]string{"josh", "board"}, "board", true},
		{[]string{"josh"}, "board", false},
		{nil, "josh", false},
		{[]string{"*", "josh"}, "board", true}, // wildcard wins regardless of order
	}
	for _, c := range cases {
		got := (&consumer{Allow: c.allow}).allows(c.upstream)
		if got != c.want {
			t.Errorf("allows(%v, %q) = %v, want %v", c.allow, c.upstream, got, c.want)
		}
	}
}

// --- unit: fixed-window rate limiter ---

func TestWindowRateLimit(t *testing.T) {
	w := &window{limit: 2, per: time.Hour}
	if !w.allow() || !w.allow() {
		t.Fatal("first two calls within limit should be allowed")
	}
	if w.allow() {
		t.Fatal("third call over the limit should be denied")
	}
	// Age the window past its span; the counter resets.
	w.start = time.Now().Add(-2 * time.Hour)
	if !w.allow() {
		t.Fatal("call after window reset should be allowed")
	}

	// limit <= 0 means unlimited.
	un := &window{limit: 0, per: time.Hour}
	for i := 0; i < 100; i++ {
		if !un.allow() {
			t.Fatal("unlimited window should never deny")
		}
	}
}

// --- unit: governor gate (auth) ---

func TestGovernorGate(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// No tokens configured -> open (local/dev).
	open := (&governor{byToken: map[string]*consumer{}}).gate(ok)
	if rec := serve(open, ""); rec.Code != http.StatusOK {
		t.Errorf("open gate: got %d, want 200", rec.Code)
	}

	// Tokens configured -> unknown/absent token rejected, known passes.
	t.Setenv("MESH_HUB_TOKENS", "ally:secret")
	g := newGovernor(&HubConfig{Consumers: []consumer{{Name: "ally", Allow: []string{"*"}, RatePerMin: 5}}})
	gated := g.gate(ok)
	if rec := serve(gated, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	if rec := serve(gated, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d, want 401", rec.Code)
	}
	if rec := serve(gated, "secret"); rec.Code != http.StatusOK {
		t.Errorf("good token: got %d, want 200", rec.Code)
	}
	// 6th request (cap 5/min) is rate-limited.
	var last int
	for i := 0; i < 6; i++ {
		last = serve(gated, "secret").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("over rate limit: got %d, want 429", last)
	}
}

func serve(h http.Handler, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// --- integration: federation + governance through the real MCP client ---

func TestHubFederation(t *testing.T) {
	// A fake upstream MCP node exposing one tool.
	up := server.NewMCPServer("fake-up", "0.0.1", server.WithToolCapabilities(true))
	up.AddTool(
		mcp.NewTool("ping", mcp.WithDescription("echo"), mcp.WithString("msg")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("pong:" + req.GetString("msg", "")), nil
		},
	)
	upTS := httptest.NewServer(server.NewStreamableHTTPServer(up))
	defer upTS.Close()

	// A fake plain-HTTP service to be wrapped as a single tool.
	svcTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("BOARD-OK"))
	}))
	defer svcTS.Close()

	t.Setenv("MESH_HUB_TOKENS", "full:tokfull,scoped:tokscoped")
	cfg := &HubConfig{
		NodeName: "test",
		Upstreams: []upstream{
			{Name: "up", Kind: "mcp", URL: upTS.URL + "/mcp"},
			{Name: "svc", Kind: "http", URL: svcTS.URL, Tool: "board", Description: "the board"},
		},
		Consumers: []consumer{
			{Name: "full", Allow: []string{"*"}},
			{Name: "scoped", Allow: []string{"up"}}, // deliberately not 'svc'
		},
	}
	handler, ups := newHub(cfg)
	if len(ups) != 2 {
		t.Fatalf("expected 2 upstreams registered, got %d", len(ups))
	}
	hubTS := httptest.NewServer(handler)
	defer hubTS.Close()

	// Unauthenticated client cannot even initialize (401 at the gate).
	if _, err := dialErr(hubTS.URL+"/mcp", ""); err == nil {
		t.Fatal("unauthenticated initialize should fail")
	}

	// Full-access consumer sees the whole federated surface.
	full := dial(t, hubTS.URL+"/mcp", "tokfull")
	names := toolNames(t, full)
	for _, want := range []string{"hub_info", "up__ping", "board"} {
		if !names[want] {
			t.Errorf("federated tool %q missing; got %v", want, names)
		}
	}

	// Namespaced MCP tool call is routed to the upstream and forwarded verbatim.
	if got := callText(t, full, "up__ping", map[string]any{"msg": "hi"}); got != "pong:hi" {
		t.Errorf("up__ping = %q, want %q", got, "pong:hi")
	}
	// HTTP-wrapped tool performs the GET and returns the body.
	if got := callText(t, full, "board", nil); got != "BOARD-OK" {
		t.Errorf("board = %q, want %q", got, "BOARD-OK")
	}

	// Scoped consumer is allowed 'up' but denied 'svc' at call time.
	scoped := dial(t, hubTS.URL+"/mcp", "tokscoped")
	if got := callText(t, scoped, "up__ping", map[string]any{"msg": "x"}); got != "pong:x" {
		t.Errorf("scoped up__ping = %q, want pong:x", got)
	}
	res := callTool(t, scoped, "board", nil)
	if !res.IsError || !strings.Contains(textOf(res), "not authorized") {
		t.Errorf("scoped board call should be denied, got isError=%v text=%q", res.IsError, textOf(res))
	}
}

// --- test client helpers ---

func dialErr(url, token string) (*client.Client, error) {
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	c, err := client.NewStreamableHttpClient(url, transport.WithHTTPHeaders(headers))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	var ir mcp.InitializeRequest
	ir.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	ir.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0"}
	if _, err := c.Initialize(ctx, ir); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func dial(t *testing.T, url, token string) *client.Client {
	t.Helper()
	c, err := dialErr(url, token)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func toolNames(t *testing.T, c *client.Client) map[string]bool {
	t.Helper()
	tr, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tr.Tools {
		names[tool.Name] = true
	}
	return names
}

func callTool(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func callText(t *testing.T, c *client.Client, name string, args map[string]any) string {
	t.Helper()
	return textOf(callTool(t, c, name, args))
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
