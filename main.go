// agent-mesh: a shareable "peer node" that exposes a small, curated set of an
// operator's local data as MCP tools, reachable by other people's AI agents over
// an ngrok endpoint with auth enforced at the edge.
//
// The framework here is generic and public. Each operator's private data and
// secrets live outside the repo: paths in a gitignored config.yaml, the ngrok
// token and peer tokens in the environment. Clone it, point it at your own
// stuff, hand a teammate your endpoint URL, and your agents can talk.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	"golang.ngrok.com/ngrok"
	"golang.ngrok.com/ngrok/config"
)

const version = "0.1.0"

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	s := server.NewMCPServer(
		"agent-mesh:"+cfg.NodeName,
		version,
		server.WithToolCapabilities(true),
	)
	registerTools(s, cfg)
	// Optional per-peer bearer-token auth. Set AGENT_MESH_TOKENS="alex:tok,bo:tok"
	// to require a token; leave unset for open local/dev use.
	var handler http.Handler = withAuth(server.NewStreamableHTTPServer(s))

	if !cfg.Ngrok.Enabled {
		log.Printf("agent-mesh node %q serving MCP locally at http://%s/mcp", cfg.NodeName, cfg.ListenAddr)
		log.Fatal(http.ListenAndServe(cfg.ListenAddr, handler))
	}

	// Exposed mode: dial out to ngrok's edge and serve MCP on the returned
	// listener (NGROK_AUTHTOKEN from env). No inbound port, no public IP.
	opts := []config.HTTPEndpointOption{}
	if cfg.Ngrok.Domain != "" {
		opts = append(opts, config.WithDomain(cfg.Ngrok.Domain))
	}
	ln, err := ngrok.Listen(context.Background(),
		config.HTTPEndpoint(opts...),
		ngrok.WithAuthtokenFromEnv(),
	)
	if err != nil {
		log.Fatalf("ngrok.Listen: %v", err)
	}
	log.Printf("agent-mesh node %q live — MCP at %s/mcp (served from this machine via ngrok-go)", cfg.NodeName, ln.URL())
	log.Fatal(http.Serve(ln, handler))
}

// withAuth gates every request on a valid "Bearer <token>" when AGENT_MESH_TOKENS
// is set (format: "name:token,name:token"). Unset = open, for local/dev use.
// Give each peer their own token so you can revoke one without touching others.
func withAuth(next http.Handler) http.Handler {
	valid := map[string]bool{}
	for _, pair := range strings.Split(os.Getenv("AGENT_MESH_TOKENS"), ",") {
		if _, tok, ok := strings.Cut(strings.TrimSpace(pair), ":"); ok && tok != "" {
			valid["Bearer "+tok] = true
		}
	}
	if len(valid) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !valid[strings.TrimSpace(r.Header.Get("Authorization"))] {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized: present a valid peer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
