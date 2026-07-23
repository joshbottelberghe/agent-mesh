# Connecting two nodes (peering)

Goal: your AI agent can call a teammate's node, and theirs can call yours. Each
person runs their own node against their own data; you exchange a URL + a token.

## Safety first — expose only a curated surface
Point your **public** node at a dedicated shared directory, never your whole home
or agent data. Run two nodes if you want both:

- **private node** — `config.yaml` → your real data, `ngrok.enabled: false` (local only, for your own agents).
- **public node** — `config.public.yaml` → a curated `~/shared` dir only; reachable by peers.

A gateway (or ngrok Traffic Policy) puts a **per-peer bearer token** in front of the
public `/mcp`. Give each peer their own token so you can revoke one independently.

## Run your public node
```sh
cp config.example.yaml config.public.yaml     # point data.* at your curated shared dir
export NGROK_AUTHTOKEN=...                      # dashboard.ngrok.com
go build -o agent-mesh .
./agent-mesh -config config.public.yaml         # or serve behind a gateway on the edge
```

## Give a peer access to your node
Send them (over a private channel):
1. your MCP URL, e.g. `https://<your-domain>/mcp`
2. the bearer token you minted for them

## Add a peer's node to your agent (Claude Code / any MCP client)
`.mcp.json`:
```json
{
  "mcpServers": {
    "ally-mesh": {
      "type": "http",
      "url": "https://<ally-domain>/mcp",
      "headers": { "Authorization": "Bearer <token-ally-gave-you>" }
    }
  }
}
```
Now `node_info`, `list_tasks`, `search_notes` on their node are available to your
agent. Two nodes + two tokens = a two-node mesh; add entries to grow to N.
