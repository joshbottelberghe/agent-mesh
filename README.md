# agent-mesh

Expose a curated set of local files as [MCP](https://modelcontextprotocol.io) tools,
reachable by another machine's AI agent over an [ngrok](https://github.com/ngrok/ngrok-go)
tunnel with per-peer token auth. The node dials outbound to ngrok's edge — no inbound
port, no public IP — and serves only the directory it is pointed at.

Built on `ngrok-go` (tunnel as a `net.Listener`) and
[`mcp-go`](https://github.com/mark3labs/mcp-go) (MCP server).

## Design
- Framework is generic; data paths live in `config.yaml` (gitignored).
- Secrets are environment variables (`NGROK_AUTHTOKEN`, `AGENT_MESH_TOKENS`), not files.
- Two nodes can share this repo, each pointed at its own data. Peers see tool results, not files.

## Run
```sh
cp config.example.yaml config.yaml     # set data paths
export NGROK_AUTHTOKEN=...              # dashboard.ngrok.com
export AGENT_MESH_TOKENS="peer:$(openssl rand -hex 24)"   # per-peer auth (optional)
go build -o agent-mesh .
./agent-mesh                           # MCP at <ngrok-url>/mcp
```
`ngrok.enabled: false` serves locally at `listen_addr`. Unset `AGENT_MESH_TOKENS` for open local use.

## Tools
| tool | description |
|------|-------------|
| `node_info` | node identity + tool list |
| `list_tasks` | tasks (title/status/priority), optional status filter |
| `search_notes` | full-text search notes; returns file + snippet |

## Auth
Per-peer bearer tokens via `AGENT_MESH_TOKENS="name:token,..."`, enforced on every request.
Edge alternative: [`traffic-policy.example.yaml`](traffic-policy.example.yaml) (token, rate
limit, `/mcp`-only, at ngrok's PoP).

## Client
`meshcli` is a minimal MCP client for testing a node:
```sh
go build -o meshcli ./cmd/meshcli
./meshcli -url https://host/mcp -token <tok> info
./meshcli -url https://host/mcp -token <tok> search "query"
```

## Peering
See [QUICKSTART.md](QUICKSTART.md) to connect two nodes, and [CONNECT.md](CONNECT.md) for the
private-node / public-node split.

## mesh-hub — federation gateway
`cmd/mesh-hub` presents **one** governed MCP endpoint that aggregates many upstreams and applies
policy at the front door — the "AI gateway" pattern at the MCP layer.

- **Upstreams** (in a gitignored `hub.yaml`, see [`hub.example.yaml`](hub.example.yaml)):
  - `kind: mcp` — dials a node and re-exposes each of its tools as `<name>__<tool>`, forwarding
    calls and preserving the original input schema.
  - `kind: http` — wraps a plain HTTP GET as a single MCP tool (turns non-MCP services into tools).
- **Governance** — per-consumer bearer auth (401), fixed-window rate limit (429), a per-upstream
  allow-list enforced at call time, and aggregate usage (consumer, tool, latency, ok) emitted to a
  collector. Consumers are bound to tokens by name via `MESH_HUB_TOKENS="name:token,..."`.
- **`hub_info`** tool maps the upstreams, their health, and what the calling consumer may reach.

```sh
cp hub.example.yaml hub.yaml            # set upstreams + consumer policy
export MESH_HUB_TOKENS="ally:$(openssl rand -hex 24)"
go build -o mesh-hub ./cmd/mesh-hub && ./mesh-hub -config hub.yaml
```

## Roadmap
- [x] MCP over local data via ngrok-go
- [x] Per-peer token auth; optional Traffic Policy edge auth
- [x] Peer client: call a configured peer's tools (`mesh-hub` federates upstream nodes)
- [x] Per-peer tool scoping (`mesh-hub` per-consumer allow-list + rate limit + usage)
