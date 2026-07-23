// mesh-hub — an MCP federation gateway.
//
// It presents ONE governed MCP endpoint that aggregates tools from many
// upstreams — other agent-mesh MCP nodes (yours, a peer's) and plain HTTP
// services wrapped as tools — and applies per-consumer auth, rate limiting,
// tool-scoping, and usage accounting at the front door. This is the "AI
// gateway" pattern at the MCP layer: one front door, many upstreams, policy in
// the middle.
//
// Like the node, the framework is generic and public; the topology and secrets
// live outside the repo. Upstreams + consumer policy go in a gitignored
// hub.yaml (see hub.example.yaml). Per-consumer tokens and any per-upstream
// tokens come from the environment, never the file.
//
//	MESH_HUB_TOKENS="ally:$(openssl rand -hex 24)"   # consumer -> token
//	MESH_UP_JOSH=<token>                             # this hub -> an upstream
//	go build -o mesh-hub ./cmd/mesh-hub && ./mesh-hub -config hub.yaml
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"golang.ngrok.com/ngrok"
	"golang.ngrok.com/ngrok/config"
	"gopkg.in/yaml.v3"
)

const version = "0.1.0"

func main() {
	cfgPath := flag.String("config", "hub.yaml", "path to hub config file")
	flag.Parse()

	cfg, err := loadHubConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	s := server.NewMCPServer("mesh-hub:"+cfg.NodeName, version, server.WithToolCapabilities(true))

	// Federate every upstream into the one hub surface. A dead upstream is
	// skipped (logged) so the hub still comes up; hub_info reports its state.
	ups := make([]*upstream, 0, len(cfg.Upstreams))
	for i := range cfg.Upstreams {
		u := &cfg.Upstreams[i]
		u.emit = cfg.emitUsage
		switch u.Kind {
		case "mcp":
			if err := u.registerMCP(s); err != nil {
				log.Printf("upstream %q (mcp): unavailable at boot — skipping its tools (%v)", u.Name, err)
			}
		case "http":
			u.registerHTTP(s)
		default:
			log.Printf("upstream %q: unknown kind %q — skipped", u.Name, u.Kind)
			continue
		}
		ups = append(ups, u)
	}
	registerHubInfo(s, cfg, ups)

	// Governance: the token->consumer map gates the door (401/429 in the HTTP
	// middleware); the resolved consumer is injected into each tool call's
	// context so handlers can enforce the per-consumer upstream allow-list.
	gov := newGovernor(cfg)
	streaming := server.NewStreamableHTTPServer(s, server.WithHTTPContextFunc(gov.inject))
	handler := gov.gate(streaming)

	if !cfg.Ngrok.Enabled {
		log.Printf("mesh-hub %q serving federated MCP locally at http://%s/mcp (%d upstreams)", cfg.NodeName, cfg.ListenAddr, len(ups))
		log.Fatal(http.ListenAndServe(cfg.ListenAddr, handler))
	}
	opts := []config.HTTPEndpointOption{}
	if cfg.Ngrok.Domain != "" {
		opts = append(opts, config.WithDomain(cfg.Ngrok.Domain))
	}
	ln, err := ngrok.Listen(context.Background(), config.HTTPEndpoint(opts...), ngrok.WithAuthtokenFromEnv())
	if err != nil {
		log.Fatalf("ngrok.Listen: %v", err)
	}
	log.Printf("mesh-hub %q live — federated MCP at %s/mcp (%d upstreams)", cfg.NodeName, ln.URL(), len(ups))
	log.Fatal(http.Serve(ln, handler))
}

// --- config ---

type HubConfig struct {
	NodeName     string     `yaml:"node_name"`
	ListenAddr   string     `yaml:"listen_addr"`
	AnalyticsURL string     `yaml:"analytics_url"` // optional: POST usage here (the analytics collector /collect)
	Ngrok        struct {
		Enabled bool   `yaml:"enabled"`
		Domain  string `yaml:"domain"`
	} `yaml:"ngrok"`
	Upstreams []upstream `yaml:"upstreams"`
	Consumers []consumer `yaml:"consumers"`
}

type consumer struct {
	Name       string   `yaml:"name"`
	Allow      []string `yaml:"allow"`        // upstream names this consumer may reach; ["*"] = all
	RatePerMin int      `yaml:"rate_per_min"` // 0 = unlimited
}

func (c *consumer) allows(upstream string) bool {
	for _, a := range c.Allow {
		if a == "*" || a == upstream {
			return true
		}
	}
	return false
}

func loadHubConfig(path string) (*HubConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c HubConfig
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.NodeName == "" {
		c.NodeName = "mesh-hub"
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:7903"
	}
	return &c, nil
}

// emitUsage fires a fire-and-forget usage event at the analytics collector.
// Aggregate metadata only (consumer, tool, latency, ok) — no tool arguments or
// results, per public-facing-professional-only. Never blocks a tool call.
func (c *HubConfig) emitUsage(consumer, tool string, ms int64, ok bool) {
	if c.AnalyticsURL == "" {
		return
	}
	body := fmt.Sprintf(`{"kind":"mesh_hub_call","source":%q,"path":%q,"extra":"ms=%d;ok=%t"}`, consumer, tool, ms, ok)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AnalyticsURL, strings.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
}

// --- upstreams ---

type upstream struct {
	Name     string `yaml:"name"`
	Kind     string `yaml:"kind"` // "mcp" | "http"
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env"` // env var holding a bearer token to reach this upstream

	// http-only:
	Tool        string `yaml:"tool"`        // tool name to expose (defaults to the upstream name)
	Description string `yaml:"description"`  // what the wrapped service returns

	emit func(consumer, tool string, ms int64, ok bool)

	mu    sync.Mutex
	cli   *client.Client // mcp only; lazily (re)connected
	tools int            // count discovered at boot, for hub_info
	up    bool
}

// registerMCP dials the upstream MCP node, discovers its tools, and re-exposes
// each under "<name>__<tool>", forwarding calls and preserving the original
// input schema so arguments pass through unchanged.
func (u *upstream) registerMCP(s *server.MCPServer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := u.connect(ctx); err != nil {
		return err
	}
	tr, err := u.cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return err
	}
	for _, t := range tr.Tools {
		realName := t.Name
		proxy := t // copy: keep Description + InputSchema, rename
		proxy.Name = u.Name + "__" + realName
		proxy.Description = fmt.Sprintf("[via %s] %s", u.Name, t.Description)
		s.AddTool(proxy, u.callHandler(realName))
	}
	u.tools = len(tr.Tools)
	u.up = true
	return nil
}

func (u *upstream) connect(ctx context.Context) error {
	headers := map[string]string{"ngrok-skip-browser-warning": "1"}
	if u.TokenEnv != "" {
		if tok := os.Getenv(u.TokenEnv); tok != "" {
			headers["Authorization"] = "Bearer " + tok
		}
	}
	c, err := client.NewStreamableHttpClient(u.URL, transport.WithHTTPHeaders(headers))
	if err != nil {
		return err
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "mesh-hub", Version: version}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		c.Close()
		return err
	}
	u.cli = c
	return nil
}

// callHandler proxies one federated tool call to its upstream, enforcing the
// caller's allow-list and recording usage. realName is the upstream's own name
// for the tool (the hub exposes it namespaced).
func (u *upstream) callHandler(realName string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		caller := consumerFrom(ctx)
		if caller != nil && !caller.allows(u.Name) {
			return mcp.NewToolResultError(fmt.Sprintf("consumer %q is not authorized for upstream %q", caller.Name, u.Name)), nil
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.cli == nil {
			if err := u.connect(ctx); err != nil {
				u.report(caller, realName, start, false)
				return mcp.NewToolResultError(fmt.Sprintf("upstream %q unreachable: %v", u.Name, err)), nil
			}
		}
		var out mcp.CallToolRequest
		out.Params.Name = realName
		out.Params.Arguments = req.Params.Arguments
		res, err := u.cli.CallTool(ctx, out)
		if err != nil {
			// one reconnect attempt — upstreams restart, tunnels flap
			u.cli.Close()
			u.cli = nil
			if err := u.connect(ctx); err == nil {
				res, err = u.cli.CallTool(ctx, out)
			}
			if err != nil {
				u.report(caller, realName, start, false)
				return mcp.NewToolResultError(fmt.Sprintf("upstream %q call failed: %v", u.Name, err)), nil
			}
		}
		u.report(caller, realName, start, !res.IsError)
		return res, nil
	}
}

// registerHTTP wraps a plain HTTP service as a single MCP tool: calling the
// tool performs a GET and returns the response body as text. Turns non-MCP
// services (render /stats, gateway /whoami, board /api/board) into first-class
// tools on the federated surface.
func (u *upstream) registerHTTP(s *server.MCPServer) {
	name := u.Tool
	if name == "" {
		name = u.Name
	}
	desc := u.Description
	if desc == "" {
		desc = fmt.Sprintf("[via %s] fetch %s", u.Name, u.URL)
	}
	s.AddTool(mcp.NewTool(name, mcp.WithDescription(desc)), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		caller := consumerFrom(ctx)
		if caller != nil && !caller.allows(u.Name) {
			return mcp.NewToolResultError(fmt.Sprintf("consumer %q is not authorized for upstream %q", caller.Name, u.Name)), nil
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.URL, nil)
		if err != nil {
			u.report(caller, name, start, false)
			return mcp.NewToolResultError(err.Error()), nil
		}
		hreq.Header.Set("ngrok-skip-browser-warning", "1")
		resp, err := http.DefaultClient.Do(hreq)
		if err != nil {
			u.report(caller, name, start, false)
			return mcp.NewToolResultError(fmt.Sprintf("upstream %q unreachable: %v", u.Name, err)), nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		u.report(caller, name, start, resp.StatusCode < 400)
		return mcp.NewToolResultText(string(bytes.TrimSpace(b))), nil
	})
	u.tools = 1
	u.up = true
}

func (u *upstream) report(c *consumer, tool string, start time.Time, ok bool) {
	if u.emit == nil {
		return
	}
	who := "anon"
	if c != nil {
		who = c.Name
	}
	u.emit(who, u.Name+"__"+tool, time.Since(start).Milliseconds(), ok)
}

// registerHubInfo adds a hub_info tool: the map of the federated surface —
// upstreams, health, tool counts, and the calling consumer's allowed set.
func registerHubInfo(s *server.MCPServer, cfg *HubConfig, ups []*upstream) {
	s.AddTool(
		mcp.NewTool("hub_info", mcp.WithDescription("Map this federation hub: upstreams, health, tool counts, and what you (the caller) are allowed to reach. Call this first.")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var b strings.Builder
			fmt.Fprintf(&b, "hub: %s\nupstreams:\n", cfg.NodeName)
			total := 0
			for _, u := range ups {
				state := "down"
				if u.up {
					state = "up"
				}
				fmt.Fprintf(&b, "  - %s (%s) %s — %d tool(s)\n", u.Name, u.Kind, state, u.tools)
				total += u.tools
			}
			fmt.Fprintf(&b, "federated tools: %d\n", total)
			if c := consumerFrom(ctx); c != nil {
				fmt.Fprintf(&b, "you: %s — allowed upstreams: %s", c.Name, strings.Join(c.Allow, ", "))
			} else {
				b.WriteString("you: unauthenticated (open/local) — all upstreams reachable")
			}
			return mcp.NewToolResultText(b.String()), nil
		},
	)
}

// --- governance (auth + rate limit + consumer identity) ---

type ctxKey struct{}

func consumerFrom(ctx context.Context) *consumer {
	c, _ := ctx.Value(ctxKey{}).(*consumer)
	return c
}

type governor struct {
	byToken  map[string]*consumer // "Bearer <tok>" -> consumer
	limiters map[string]*window   // consumer name -> rate window
	mu       sync.Mutex
}

func newGovernor(cfg *HubConfig) *governor {
	g := &governor{byToken: map[string]*consumer{}, limiters: map[string]*window{}}
	policy := map[string]*consumer{}
	for i := range cfg.Consumers {
		policy[cfg.Consumers[i].Name] = &cfg.Consumers[i]
	}
	// MESH_HUB_TOKENS="name:token,name:token" binds a token to a named policy.
	for _, pair := range strings.Split(os.Getenv("MESH_HUB_TOKENS"), ",") {
		name, tok, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok || tok == "" {
			continue
		}
		c := policy[name]
		if c == nil {
			// token with no explicit policy: known caller, all upstreams, no cap
			c = &consumer{Name: name, Allow: []string{"*"}}
		}
		g.byToken["Bearer "+tok] = c
		g.limiters[name] = &window{limit: c.RatePerMin, per: time.Minute}
	}
	return g
}

// gate is the HTTP front door: 401 unknown/absent token (when any token is
// configured), 429 over the consumer's per-minute rate. No tokens configured =
// open, for local/dev use (matches the node's behavior).
func (g *governor) gate(next http.Handler) http.Handler {
	if len(g.byToken) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := g.byToken[strings.TrimSpace(r.Header.Get("Authorization"))]
		if c == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized: present a valid consumer token", http.StatusUnauthorized)
			return
		}
		g.mu.Lock()
		lim := g.limiters[c.Name]
		g.mu.Unlock()
		if lim != nil && !lim.allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// inject resolves the token to its consumer and stashes it on the call context
// so tool handlers can enforce the per-upstream allow-list.
func (g *governor) inject(ctx context.Context, r *http.Request) context.Context {
	if c := g.byToken[strings.TrimSpace(r.Header.Get("Authorization"))]; c != nil {
		return context.WithValue(ctx, ctxKey{}, c)
	}
	return ctx
}

// window is a minimal fixed-window rate limiter (per consumer).
type window struct {
	mu    sync.Mutex
	limit int
	per   time.Duration
	start time.Time
	count int
}

func (w *window) allow() bool {
	if w.limit <= 0 {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if now.Sub(w.start) >= w.per {
		w.start = now
		w.count = 0
	}
	if w.count >= w.limit {
		return false
	}
	w.count++
	return true
}
