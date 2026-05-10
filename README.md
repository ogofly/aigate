# aigate

Minimal Go gateway for OpenAI-like LLM APIs.

## Features

- OpenAI-compatible `POST /v1/chat/completions`
- Supports non-stream and `stream=true`
- OpenAI-compatible `POST /v1/embeddings`
- OpenAI-compatible `GET /v1/models`
- OpenAI-compatible `GET /v1/models/{model}`
- Configurable model-to-provider mapping
- Client API key authentication
- Basic stdout logs for request routing and upstream errors
- SQLite-backed provider/model/auth-key config
- SQLite-backed aggregated usage persistence
- Simple web admin for full provider/model/auth-key config and usage view

## Quick Start

1. Prepare local env.

```bash
cp .env.example .env
```

2. Start the server.

```bash
go run ./cmd/aigate -config config.example.json
```

3. Open the admin.

```text
http://localhost:8080/admin/login
```

Default example credentials:

```text
username: admin
password: admin123
```

4. Add one provider, one model, and one key in the admin UI.

Use these fields:
- Provider: `name`, `base_url`, `api_key` or `api_key_ref`, `timeout`
- Model: `public_name`, `provider`, `upstream_name`
- Key: `key`, `name`, `owner` optional, `purpose`

Use either:
- `api_key`: the real upstream secret
- `api_key_ref`: an environment variable name such as `OPENAI_API_KEY`

5. Call the gateway.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer sk-app-001' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "hello"}
    ]
  }'
```

6. Stream mode.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer sk-app-001' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o-mini",
    "stream": true,
    "messages": [
      {"role": "user", "content": "hello"}
    ]
  }'
```

7. Embeddings.

```bash
curl http://localhost:8080/v1/embeddings \
  -H 'Authorization: Bearer sk-app-001' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "text-embedding-3-small",
    "input": "hello"
  }'
```

8. Read usage for the current key.

```bash
curl http://localhost:8080/v1/usage \
  -H 'Authorization: Bearer sk-app-001'
```

Query usage with REST filters:

```bash
curl "http://localhost:8080/v1/usage?view=by_model&model=gpt-4o-mini" \
  -H 'Authorization: Bearer sk-app-001'
```

9. Read usage for all keys in the web admin.

Open:

```text
http://localhost:8080/admin/usage/view
```

## Config

Config is JSON for now. Example: [config.example.json](./config.example.json)

The server loads `.env` if present, but existing process environment variables win.

`auth.keys` supports either a plain string or an object:

```json
{
  "key": "sk-app-001",
  "name": "alice-dev",
  "owner": "alice",
  "purpose": "internal-debug"
}
```

`owner` is optional.

Admin credentials are configured in `admin.username` and `admin.password`.

SQLite storage is configured in `storage.sqlite_path`.
`storage.flush_interval` uses seconds and controls how often aggregated usage is flushed to SQLite.
Usage rollups in SQLite store `key_id`, not the raw API key.

Provider config supports either `api_key` or `api_key_ref`.
If both are set, `api_key` is used first.

Each public model maps to exactly one provider:

```json
{
  "public_name": "gpt-4o-mini",
  "provider": "openai",
  "upstream_name": "gpt-4o-mini"
}
```

`providers[].timeout` uses seconds.

## Endpoints

- `GET /healthz`
- `GET /v1/models`
- `GET /v1/models/{model}`
- `GET /v1/usage`
- `GET /v1/usage?view=by_model|trend&start=YYYY-MM-DD&end=YYYY-MM-DD&model=...`
- `GET /admin/usage`
- `GET /admin/login`
- `GET /admin/keys`
- `GET /admin/models`
- `GET /admin/usage/view`
- `POST /v1/chat/completions`
- `POST /v1/embeddings`

## Test

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod-cache go test ./...
```
