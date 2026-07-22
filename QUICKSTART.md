# agent-mesh — Quickstart

Run a node that serves a curated folder as MCP tools over an ngrok tunnel, then connect it to
a peer's node. Result: your MCP client can call `search_notes` / `list_tasks` on the peer's
machine, and vice versa. Access is limited to the shared folder and gated by a per-peer token.

## Prerequisites
Go 1.23+ · an ngrok account + authtoken (`dashboard.ngrok.com`) · an MCP client (e.g. Claude Code).

## 1. Build
```sh
git clone https://github.com/joshbottelberghe/agent-mesh
cd agent-mesh && go build -o agent-mesh .
```

## 2. Curate a shared folder
```sh
mkdir -p ~/agent-mesh-shared/notes ~/agent-mesh-shared/tasks
cp config.example.yaml config.yaml
```
In `config.yaml`: set `ngrok.enabled: true` and point `data.*` at `~/agent-mesh-shared` only —
never your home or private notes. Optionally reserve a domain (`dashboard.ngrok.com/domains`)
and set `ngrok.domain`.

## 3. Run with a per-peer token
```sh
export NGROK_AUTHTOKEN=...
export AGENT_MESH_TOKENS="peer:$(openssl rand -hex 24)"
./agent-mesh -config config.yaml
```
The log prints your public URL (`.../mcp`). That URL plus the token is what you send the peer.

## 4. Exchange and connect
Send the peer your `/mcp` URL and their token over a private channel; get theirs in return.
Add their node to your MCP client (`.mcp.json`):
```json
{ "mcpServers": { "peer-mesh": {
  "type": "http",
  "url": "<PEER_MCP_URL>",
  "headers": { "Authorization": "Bearer <TOKEN_FROM_PEER>" }
} } }
```

## Verify
```sh
curl -s -X POST "<PEER_MCP_URL>" \
  -H "Authorization: Bearer <TOKEN_FROM_PEER>" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'ngrok-skip-browser-warning: 1' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"client","version":"0"}}}'
```
Expected: `"serverInfo":{"name":"agent-mesh:<peer-node>", ...}`. A wrong token returns `401`;
an offline machine fails to connect.

## Tools
`node_info`, `list_tasks`, `search_notes`. Add more in `tools.go`.
