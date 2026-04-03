# aigate

Minimal Go gateway for OpenAI-like LLM APIs with static multi-provider routing.

## Features

- OpenAI-compatible `POST /v1/chat/completions`
- Supports non-stream and `stream=true`
- OpenAI-compatible `POST /v1/embeddings`
- OpenAI-compatible `GET /v1/models`
- Static model-to-provider mapping
- Client API key authentication
- Basic stdout logs for request routing and upstream errors
- In-memory usage stats by key

## Quick Start

1. Prepare env vars.

You can use shell env vars directly, or copy `.env.example` to `.env` for local development.

```bash
cp .env.example .env
```

```bash
export OPENAI_API_KEY=your-openai-key
export DEEPSEEK_API_KEY=your-deepseek-key
```

2. Start the server.

```bash
go run ./cmd/aigate -config configs/aigate.example.json
```

3. Call the gateway.

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

4. Stream mode.

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

5. Embeddings.

```bash
curl http://localhost:8080/v1/embeddings \
  -H 'Authorization: Bearer sk-app-001' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "text-embedding-3-small",
    "input": "hello"
  }'
```

6. Read usage for the current key.

```bash
curl http://localhost:8080/v1/usage \
  -H 'Authorization: Bearer sk-app-001'
```

7. Read usage for all keys with an admin key.

```bash
curl http://localhost:8080/admin/usage \
  -H 'Authorization: Bearer sk-admin-001'
```

## Config

Config is JSON for now. Example: [configs/aigate.example.json](/Users/liuyc/code/aigate/configs/aigate.example.json)

The server loads `.env` if present, but existing process environment variables win.

`auth.keys` supports either a plain string or an object:

```json
{
  "key": "sk-app-001",
  "name": "alice-dev",
  "owner": "alice",
  "purpose": "internal-debug",
  "admin": false
}
```

`owner` is optional.

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
- `GET /v1/usage`
- `GET /admin/usage`
- `POST /v1/chat/completions`
- `POST /v1/embeddings`

## Test

```bash
GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod-cache go test ./...
```
