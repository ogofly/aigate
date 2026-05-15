# aigate

A lightweight Go gateway for LLM APIs. Put OpenAI-compatible, Anthropic-compatible, and Responses-style traffic behind one clean entrypoint with routing, API keys, usage analytics, and a built-in admin UI.

## Why aigate

- **One gateway, multiple API styles**: OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, Embeddings, and Models.
- **Model routing without client changes**: map public model names to upstream providers and models; switch providers from the admin UI.
- **Operational controls built in**: API keys, per-key model access, provider credentials, routing strategy, failover, and usage analytics.
- **Useful admin UI**: manage providers, models, keys, playground tests, dark/light theme, and token/request charts.
- **Simple deployment**: a single Go service with SQLite storage and `.env` support.

## Screenshots

### Light Theme

**Playground** — test requests with managed keys, switch between OpenAI/Anthropic styles:

<a href="./docs/screenshots/playground-light.jpeg"><img src="./docs/screenshots/playground-light.jpeg" width="48%" /></a>

**Usage**

<a href="./docs/screenshots/usage-light.jpeg"><img src="./docs/screenshots/usage-light.jpeg" width="48%" /></a>

### Dark Theme

**Login**

<a href="./docs/screenshots/login-dark.jpeg"><img src="./docs/screenshots/login-dark.jpeg" width="48%" /></a>

**Usage Analytics:**  
<a href="./docs/screenshots/usage-dark.jpeg"><img src="./docs/screenshots/usage-dark.jpeg" width="48%" /></a>

## Quick Start

```bash
cp .env.example .env
go run ./cmd/aigate -config config.example.json
```

Open the admin UI:

```text
http://localhost:8080/admin/login
username: admin
password: admin123
```

Then create these in the admin UI:

- **Provider**: upstream `base_url` plus `api_key` or `api_key_ref`
- **Model**: public model alias mapped to a provider and upstream model
- **API Key**: client key with optional owner, purpose, and model access

## API Examples

Chat Completions:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer sk-app-001' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

Streaming uses the same endpoint with `"stream": true`.

Embeddings:

```bash
curl http://localhost:8080/v1/embeddings \
  -H 'Authorization: Bearer sk-app-001' \
  -H 'Content-Type: application/json' \
  -d '{"model": "text-embedding-3-small", "input": "hello"}'
```

Usage summary:

```bash
curl http://localhost:8080/v1/usage \
  -H 'Authorization: Bearer sk-app-001'
```

## Supported Endpoints

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `GET /v1/models`
- `POST /anthropic/v1/messages`
- `GET /v1/usage`
- `GET /admin/login`

## Configuration

Start from [config.example.json](./config.example.json). The service also loads `.env` when present; process environment variables take precedence.

Key settings:

- `admin.username` / `admin.password`: admin login
- `storage.sqlite_path`: SQLite database path
- `storage.flush_interval`: usage flush interval in seconds
- `providers[]`: upstream credentials via inline `api_key` or env `api_key_ref`
- `models[]`: public model routing to provider/upstream model
- `routing`: `priority`, `weight`, or `random` selection with optional failover

## Test

```bash
go test ./...
```
