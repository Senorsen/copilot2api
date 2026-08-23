# copilot2api

A lightweight Go proxy that exposes GitHub Copilot as OpenAI-compatible, Anthropic-compatible, Gemini-compatible, and AmpCode-compatible API endpoints.

## Features

- **OpenAI API Compatible**: `/v1/chat/completions`, `/v1/models`, `/v1/embeddings`, `/v1/responses`
- **Anthropic API Compatible**: `/v1/messages`, `/v1/messages/count_tokens`
- **Gemini API Compatible**: `/v1beta/models`, `/v1beta/models/{model}:generateContent`, etc.
- **AmpCode Compatible**: `/amp/v1/*` routes
- **Streaming Support**: Full SSE streaming for OpenAI and Anthropic formats
- **Auto Authentication**: GitHub Device Flow OAuth with automatic token refresh
- **Prompt Caching**: `cache_control` fields are passed through transparently
- **1M Context**: `anthropic-beta: context-1m-*` header auto-appends `-1m` to model ID
- **Thinking Mode**: `thinking.type: "enabled"` is rewritten to `"adaptive"` for Claude Code compatibility
- **Usage Monitoring**: Built-in `/usage` endpoint (Control Plane) for quota tracking
- **Multi-Client Authentication**: Multiple data-plane API keys with per-client usage tracking
- **Multi-Account** *(fork feature)*: One process manages multiple Copilot accounts with a Control Plane API

---

## Quick Start

### Docker

```bash
docker run -it --rm \
  -p 127.0.0.1:7777:7777 \
  -p 127.0.0.1:7778:7778 \
  -v ~/.config/copilot2api:/root/.config/copilot2api \
  -e API_TOKEN=your-api-token-here \
  -e ADMIN_TOKEN=your-admin-token-here \
  ghcr.io/senorsen/copilot2api:latest
```

<details>
<summary>Docker Compose</summary>

```yaml
services:
  copilot2api:
    image: ghcr.io/senorsen/copilot2api:latest
    ports:
      - "127.0.0.1:7777:7777"
      - "127.0.0.1:7778:7778"
    volumes:
      - ${HOME}/.config/copilot2api:/root/.config/copilot2api
    environment:
      API_TOKEN: your-api-token-here
      ADMIN_TOKEN: your-admin-token-here
```

</details>

### Binary

```bash
# Linux x64
curl -L -o copilot2api \
  https://github.com/Senorsen/copilot2api/releases/latest/download/copilot2api-linux-amd64
chmod +x copilot2api

API_TOKEN=your-api-token-here ADMIN_TOKEN=your-admin-token-here ./copilot2api
```

---

## Multi-Account Architecture *(fork feature)*

This fork manages **multiple GitHub Copilot accounts** in a single process. Each account has its own credentials stored under a UUID v4 directory:

```
~/.config/copilot2api/
├── 550e8400-e29b-41d4-a716-446655440000/
│   └── credentials.json   ← auto-created after device flow
├── 6ba7b810-9dad-11d1-80b4-00c04fd430c8/
│   └── credentials.json
└── ...
```

Accounts are managed via the **Control Plane API** (port 7778). The **Data Plane API** (port 7777) routes requests to a specific account by account ID in the URL.

---

## Data Plane API — Port 7777

All inference requests go through this port. The URL includes the account ID:

```
/api/{account_id}/v1/...
```

### Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/api/{account_id}/v1/messages` | POST | Anthropic Messages API |
| `/api/{account_id}/v1/messages/count_tokens` | POST | Anthropic token counting |
| `/api/{account_id}/v1/chat/completions` | POST | OpenAI Chat Completions |
| `/api/{account_id}/v1/models` | GET | List available models |
| `/api/{account_id}/v1/*` | ANY | Other paths are proxied as-is |
| `/gw/api/v1/messages` | POST | Gateway: Anthropic Messages (load-balanced) |
| `/gw/api/v1/chat/completions` | POST | Gateway: OpenAI Chat Completions (load-balanced) |

### Gateway Routes (Load-Balanced)

The `/gw/api/...` routes act as a **load-balancing gateway** — they automatically select from all logged-in accounts on each request, with retry on failure.

**Routes:**

| Path | Description |
|------|-------------|
| `/gw/api/v1/messages` | Anthropic Messages API (load-balanced) |
| `/gw/api/v1/chat/completions` | OpenAI Chat Completions (load-balanced) |

**Features:**

- **Model-aware account selection**: Before random selection, the gateway checks each account's cached `/models` list and only chooses among accounts that currently advertise the requested model.
- **Random account selection** across the resulting model-compatible pool.
- **IP affinity**: Within a 1-hour window, the same `(client IP, model)` pair preferentially reuses the same compatible account.
  - *Anthropic*: Affinity only applies when the incoming request carries `cache_control`.
  - *OpenAI*: Affinity always applies.
- **Reactive model fallback**: If an account still returns `400 The requested model is not supported.` (for example because its cached availability changed), the gateway invalidates that account's model cache and retries another compatible account. Streaming success responses remain streamed rather than buffered.
- **`GW_EXCLUDE` environment variable**: Comma-separated list of `account_id` values to exclude from gateway routing (e.g. `GW_EXCLUDE=uuid1,uuid2`).

**Authentication:** Same as the Data Plane — use a configured API token via `Authorization: Bearer` or `x-api-key`.

### Authentication

Configure exactly one of `API_TOKEN` (single client token) or `API_TOKENS` (multiple client tokens). `API_TOKENS` uses whitespace-separated `id:token` entries:

```bash
API_TOKENS='alice:alice-secret bob:bob-secret'
```

The first colon separates the client ID, so a token may contain additional colons or commas, but not whitespace. Client IDs and tokens must both be unique. Setting both variables makes startup fail.

Requests must include one of:

```
Authorization: Bearer your-api-token-here
x-api-key: your-api-token-here
```

If neither `API_TOKEN` nor `API_TOKENS` is configured, authentication is skipped (dev mode). Requests authenticated with `API_TOKEN` are attributed to client ID `default`.

### Example

```bash
# Anthropic Messages via account ID
curl http://localhost:7777/api/550e8400-e29b-41d4-a716-446655440000/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-token-here" \
  -d '{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"Hello!"}],"max_tokens":100}'

# OpenAI Chat Completions via account ID
curl http://localhost:7777/api/550e8400-e29b-41d4-a716-446655440000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-token-here" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello!"}]}'
```

---

## Control Plane API — Port 7778

Account management runs on a separate port. All endpoints require:

```
Authorization: Bearer your-admin-token-here
```

Set `ADMIN_TOKEN` environment variable. If empty, authentication is skipped (dev mode).

### Endpoints

#### Add a new account

```
POST /accounts/login
```

Initiates GitHub Device Flow. Returns a `progress_id` and the device code URL.

Optional body:
```json
{ "username_suffix": "@yourorg.com" }
```

Use `username_suffix` to restrict which GitHub account can complete the flow.

Response:
```json
{
  "progress_id": "...",
  "user_code": "XXXX-XXXX",
  "verification_uri": "https://github.com/login/device"
}
```

#### Poll login progress

```
GET /accounts/{progress_id}/status
```

Poll until status changes from `"pending"`. Possible statuses:

| Status | Description |
|--------|-------------|
| `pending` | Waiting for user to complete device code flow |
| `completed` | Login successful, returns `account_id` and `github_username` |
| `expired` | Device code timed out, need to start a new login |
| `error` | Login failed (e.g. username suffix mismatch), includes `error` message |

**Success:**
```json
{
  "status": "completed",
  "account_id": "550e8400-e29b-41d4-a716-446655440000",
  "github_username": "yourname"
}
```

**Error (e.g. username suffix mismatch):**
```json
{
  "status": "error",
  "error": "username \"john\" does not end with required suffix \"_microsoft\"",
  "github_username": "john"
}
```

**Expired:**
```json
{
  "status": "expired"
}
```

#### List all accounts

```
GET /accounts
```

Returns all registered accounts with their GitHub username and account ID.

#### Delete an account

```
DELETE /accounts/{account_id}
```

Removes the account and its credentials from disk.

#### Usage / Quota

```
GET /usage
```

Returns quota usage across all registered accounts.

#### Usage Statistics (opt-in)

Enable with `COPILOT2API_STATS_ENABLED=true`. Records per-request token usage to JSONL files. Timestamps and file date partitions are always stored in UTC, independently of the host or container timezone.

```
GET /dashboard
```

Interactive HTML dashboard with stacked bar charts, estimated costs (via LiteLLM pricing data), time range selectors, combinable client/model/account/reasoning-effort dimensions and filters, and auto-refresh. Requires `ADMIN_TOKEN` entered in the UI (stored in localStorage).

```
GET /usage?start=YYYY-MM-DD&end=YYYY-MM-DD[&client_id=...][&account_id=...][&model=...][&reasoning_effort=...]
```

Returns aggregated daily token stats (input, output, cached) per client/model/account/reasoning effort. Historical records, unauthenticated requests, and requests authenticated with `API_TOKEN` are attributed to `default`. Reasoning effort is the client-requested value when provided explicitly, or a classification derived from a supplied thinking budget; requests without either are reported as `unspecified`. Usage stats cover recorded OpenAI-compatible and Anthropic traffic; native Gemini traffic is not currently recorded. Requires `ADMIN_TOKEN` via `Authorization: Bearer` header.

```
GET /usage/accounts
```

Lists accounts that have usage data. Requires `ADMIN_TOKEN`.

```
GET /usage/pricing
```

Returns cached LiteLLM model pricing JSON. Automatically fetched on startup and refreshed daily at 3:00 AM UTC. Requires `ADMIN_TOKEN`.

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `API_TOKEN` | Single data-plane auth token; mutually exclusive with `API_TOKENS` | — |
| `API_TOKENS` | Whitespace-separated data-plane `id:token` entries; mutually exclusive with `API_TOKEN` | — |
| `ADMIN_TOKEN` | Control plane auth token (optional) | — |
| `GW_EXCLUDE` | Comma-separated `account_id` list to exclude from gateway routing | — |
| `COPILOT2API_TOKEN_DIR` | Credentials storage directory | `~/.config/copilot2api` |
| `COPILOT2API_HOST` | Server host | `127.0.0.1` |
| `COPILOT2API_PORT` | Data plane port | `7777` |
| `COPILOT2API_CONTROL_PORT` | Control plane port | `7778` |
| `COPILOT2API_DEBUG` | Enable debug logging | `false` |
| `COPILOT2API_STATS_ENABLED` | Enable usage stats recording, dashboard, and pricing | `false` |
| `COPILOT2API_STATS_DIR` | Stats data directory | `~/.config/copilot2api/stats` |

---

## Usage with Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:7777/api/your-account-id-here",
    "ANTHROPIC_API_KEY": "your-api-token-here",
    "ANTHROPIC_MODEL": "claude-opus-4.6",
    "ANTHROPIC_SMALL_FAST_MODEL": "claude-haiku-4.5",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```

### 1M Context Window

Capable Claude models (opus/sonnet 4.6+) always get the `anthropic-beta: context-1m-2025-08-07` header injected upstream, so requests use the full 1M context window instead of falling back to the 200k limit. Any client-supplied `anthropic-beta` value is merged rather than replaced.

### Thinking Mode

`thinking.type: "enabled"` is automatically rewritten to `"adaptive"` for compatibility with Claude Code's extended thinking requests.

---

## Usage with Codex

Add to `~/.codex/config.toml`:

```toml
model = "gpt-5.3-codex"
model_provider = "copilot2api"
model_reasoning_effort = "high"
web_search = "disabled"
requires_openai_auth = false

[model_providers.copilot2api]
name = "copilot2api"
base_url = "http://127.0.0.1:7777/api/your-account-id-here/v1"
wire_api = "responses"
api_key = "your-api-token-here"
```

---

## Usage with Gemini CLI

Add to `~/.gemini/.env`:

```env
GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:7777/api/your-account-id-here
GEMINI_API_KEY=your-api-token-here
GEMINI_MODEL=claude-opus-4.6-1m
```

---

## Docker Image

The image is built `FROM scratch` — a single static binary plus CA certificates. No shell, no OS packages, no CVEs from base layers.

```
EXPOSE 7777  # Data Plane
EXPOSE 7778  # Control Plane
```

---

## Development

```bash
go test ./...              # Run tests
go build -o copilot2api .  # Build
```

---

## License

MIT
