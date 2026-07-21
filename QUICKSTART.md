# agent-mesh — Quickstart (peer your agent with a teammate's)

## What this is (30-second version)
A teammate is inviting you to **connect your AI agents**. You each run a tiny Go
program on your own machine that publishes a **curated folder** as a few MCP tools
(the protocol Claude/Cursor use to call tools). It gets a public HTTPS URL via
ngrok — no open ports, no public IP — locked behind a **token you mint per person**.

**End state:** your Claude (or any MCP client) can call `search_notes` / `list_tasks`
on their machine, and theirs on yours — live, over the internet. Neither of you gets
access to the other's files, network, or anything outside the one folder you choose
to share. Tokens are revocable; a node is only reachable while its machine is on.

~10 minutes. You need nothing from the person who sent you this except one URL + one
token (step 4).

## Prereqs
- **Go** 1.23+  ·  **ngrok** account (free) + authtoken (`dashboard.ngrok.com/get-started/your-authtoken`)  ·  **Claude Code** (or any MCP client)

## 1. Build
```sh
git clone https://github.com/joshbottelberghe/agent-mesh
cd agent-mesh && go build -o agent-mesh .
```

## 2. Pick what to share (curated — this is the whole safety model)
```sh
mkdir -p ~/agent-mesh-shared/notes ~/agent-mesh-shared/tasks
echo "# shared" > ~/agent-mesh-shared/notes/hello.md
cp config.example.yaml config.yaml
```
Edit `config.yaml`: set `ngrok.enabled: true`, and point `data.tasks_dir` /
`data.notes_dirs` at `~/agent-mesh-shared/...` **only** (never your home or private notes).
Optional: reserve a static domain at `dashboard.ngrok.com/domains` and put it in `ngrok.domain`.

## 3. Run it (with a token per peer)
```sh
export NGROK_AUTHTOKEN=...                       # from the dashboard
export AGENT_MESH_TOKENS="josh:$(openssl rand -hex 24)"   # mint a token for Josh
./agent-mesh -config config.yaml
```
It prints your public URL, e.g. `MCP at https://<you>.ngrok-free.dev/mcp`.
Note that URL and the token you minted — that pair is what you hand Josh.

## 4. Exchange + connect
- **Send Josh** (private channel): your `.../mcp` URL + the token you minted for him.
- **Josh sends you**: his `/mcp` URL + a token for you.
- Add Josh's node to your MCP client — `.mcp.json`:
```json
{ "mcpServers": { "josh-mesh": {
  "type": "http",
  "url": "<JOSH_MCP_URL>",
  "headers": { "Authorization": "Bearer <TOKEN_JOSH_GAVE_YOU>" }
} } }
```

## ✅ Success condition
From your machine, this reaches Josh's node through the tunnel with your token:
```sh
curl -s -X POST "<JOSH_MCP_URL>" \
  -H "Authorization: Bearer <TOKEN_JOSH_GAVE_YOU>" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'ngrok-skip-browser-warning: 1' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"alex","version":"0"}}}'
```
**Pass = you see `"serverInfo":{"name":"agent-mesh:josh-shared", ...}`.** Your machine
just talked to Josh's. Then in Claude Code, ask it to call `node_info` on `josh-mesh`
and you'll get his node back — and Josh doing the same against yours = a live two-node mesh.

Wrong token → `401`. Machine off → connection fails (that's expected; nodes are only up while the machine is).

## Tools exposed
`node_info` (identity + capabilities) · `list_tasks` · `search_notes`. Extend in `tools.go`.
