# cake-proxy

A local recording proxy for the OpenAI Chat Completions and Responses APIs. It
forwards requests to one upstream provider, streams responses through
unchanged, and shows every exchange in an HTML inspector.

Point cake (or any OpenAI-compatible client) at it:

```toml
# ~/.config/cake/settings.toml
[[models]]
name = "proxied"
model = "deepseek-v4.1-flash"
base_url = "http://127.0.0.1:8787/v1"
api_key_env = "CAKE_PROXY_TOKEN"
api_type = "chat_completions"
provider = "opencode"          # keep provider-specific headers when proxying Zen
```

```sh
export CAKE_PROXY_TOKEN=proxy  # any non-empty placeholder
cake-proxy \
  -upstream https://opencode.ai/inference/go/openai/v1 \
  -upstream-key-env OPENCODE_ZEN_API_TOKEN
```

Open <http://127.0.0.1:8787/> to inspect requests as they arrive.

## What the inspector shows

- one row per request, newest first, with status, API, model, token usage, and
  duration; in-flight rows update while the provider streams
- the full request body rendered as messages and tool calls, plus the raw JSON
- the assembled response, the raw body for non-streamed responses, and the
  streamed events with their exact JSON payloads
- request and response headers with credentials redacted
- provider-reported usage, finish reason, time to first byte, and capture
  truncation markers

Everything is local. The UI has no authentication, so the proxy binds
`127.0.0.1` and refuses a non-loopback address unless `-allow-remote` is set.

## Credentials

By default the client's credential is forwarded untouched. With
`-upstream-key-env OPENCODE_ZEN_API_TOKEN`, the proxy replaces the client's
credential with the value of that environment variable. That lets cake keep a
dummy `api_key_env` value while the real key never enters the cake process.

The real key is never written to the capture store. Credential headers are
stored as `<redacted>`, and sensitive query parameters (`api_key`, `key`,
`token`, `access_token`, `authorization`) are stored as `redacted`. Request and
response bodies are stored verbatim, so the store contains prompts, tool
output, and model responses: treat it as sensitive.

## Endpoints and path mapping

| Proxy path | Upstream path |
| --- | --- |
| `/v1/chat/completions` | `<upstream>/chat/completions` |
| `/v1/responses` | `<upstream>/responses` |
| `/v1/<anything>` | `<upstream>/<anything>` |
| `/chat/completions`, `/responses` | same, without a `/v1` mount |

The upstream flag is the base URL that endpoints live under. For example,
`-upstream https://openrouter.ai/api/v1` plus `/v1/chat/completions` becomes
`https://openrouter.ai/api/v1/chat/completions`.

Server-sent event streams are flushed byte for byte as they arrive; they are
never buffered or re-serialized. Non-streaming bodies are relayed verbatim.
Compression is disabled on the upstream request so captured streams stay
readable.

## Storage

Finished exchanges are written as one JSON document each under
`~/.local/share/cake-proxy/exchanges/` (`$CAKE_PROXY_DATA_DIR` or `-data-dir`
override). The default retains the newest 200 finished exchanges; use `-keep 0`
to keep all or a smaller number to bound disk use. Set `-data-dir ""` for a
memory-only run. Files are created with mode `0600` in a `0700` directory.

## Limitations

- **Not for the Codex ChatGPT subscription backend.** Cake keys its Codex
  request shaping and auth on the exact `base_url`; pointing cake at the proxy
  hides that URL, so the proxy cannot stand in for `chatgpt.com/backend-api/codex`.
  Use it with ordinary OpenAI-compatible API keys.
- **Provider inference is lost.** Cake infers OpenCode/OpenRouter behavior from
  the base URL. Add `provider = "opencode"` (or `"openrouter"`) to the proxied
  `[[models]]` entry to keep those headers.
- **Resuming is model-entry bound.** Sessions pin the `[[models]]` entry name
  and its `base_url`. A new proxy-backed entry starts a new session; to move an
  existing session, resume with `--allow-session-model-change` or start over.
- One upstream per proxy process. Run another instance on another port for a
  second provider.
- The inspector is unauthenticated by design and intended for loopback only.

## Development

```sh
just hooks
just ci          # gofmt, vet, tests, JS syntax, build, version
just test-race
just build
just install     # go install into GOBIN
```

Source layout:

- `cmd/cake-proxy` — flags, server wiring, startup message
- `internal/proxy` — transparent relay and stream capture
- `internal/capture` — in-memory and on-disk exchange store
- `internal/openai` — usage/model/output-text extraction for both APIs
- `internal/sse` — incremental server-sent event parser
- `internal/web` — embedded inspector UI and JSON API
