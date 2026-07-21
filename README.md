# agent-mesh

A small, shareable **peer node**: it exposes a curated slice of your local data
as **MCP tools**, reachable by other people's AI agents over an **ngrok** endpoint
with **auth enforced at the edge**. Run one, hand a teammate your URL + a token,
and your agents can query each other.

Built with [`ngrok-go`](https://github.com/ngrok/ngrok-go) (the tunnel is a
`net.Listener` on ngrok's cloud) and [`mcp-go`](https://github.com/mark3labs/mcp-go)
(the MCP server). No inbound port, no public IP, nothing exposed on your network.

## The public / private split (why this is safe to share)
- **The repo is generic framework.** Tools read from whatever paths you configure.
- **Your data stays yours.** Paths live in `config.yaml` (**gitignored**); it never
  enters the repo. The sample `./data` is the only data that ships.
- **Secrets live in the environment.** `NGROK_AUTHTOKEN` and per-peer tokens are
  env vars, not files. So both the repo *and* your `config.yaml` are secret-free.

You and a teammate share this same repo, each point it at your own private data,
and neither of you ever sees the other's files — only the tool results you each
choose to expose.

**New here? See [QUICKSTART.md](QUICKSTART.md)** for a 10-minute peer-with-a-teammate walkthrough.

## Run it
```sh
cp config.example.yaml config.yaml       # then edit paths
export NGROK_AUTHTOKEN=...                # from dashboard.ngrok.com
export AGENT_MESH_TOKENS="alex:$(openssl rand -hex 24)"   # optional: per-peer bearer auth
go build -o agent-mesh .
./agent-mesh                             # serves MCP at <ngrok-url>/mcp
```
Set `ngrok.enabled: false` to serve locally at `listen_addr` instead (no tunnel).
Leave `AGENT_MESH_TOKENS` unset for open local/dev use; set it (`name:token,...`) to
require `Authorization: Bearer <token>` on every request.

## Tools
- `node_info` — identify this node and list its tools (use first when peering).
- `list_tasks` — list tasks (title/status/priority), optional status filter.
- `search_notes` — full-text search your notes, returns file + snippet.

## Securing the endpoint (Traffic Policy)
See [`traffic-policy.example.yaml`](traffic-policy.example.yaml): require a per-peer
bearer token, rate-limit callers, and expose only `/mcp` — all at ngrok's edge,
before anything reaches your machine. Give each peer their own token so you can
revoke one without disturbing the others.

## Peering (phase 2)
Add a peer under `peers:` with their `url` and the env var holding their token.
Two nodes + two tokens = a two-node mesh where each agent can call the other's
tools. Grows to N by adding entries.

## Roadmap
- [x] MCP server over local data, exposed via ngrok-go
- [x] Per-peer bearer-token auth (`AGENT_MESH_TOKENS`); optional edge auth via Traffic Policy
- [ ] Peer client: call a configured peer's tools from this node
- [ ] Per-peer tool scoping (who can see which tools)
