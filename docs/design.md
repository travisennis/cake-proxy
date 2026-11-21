# Design

## Purpose

`cake-proxy` is a local, inspecting reverse proxy. Cake points one
`[[models]]` entry at `http://127.0.0.1:8787/v1`; the proxy forwards each
request to a real OpenAI-compatible provider and keeps a record of what was
sent and returned. No Cake changes are required.

The proxy is not a gateway or a policy layer. It does not route by model, add
tools, transform prompts, or retry requests.

## Request flow

1. `ServeHTTP` accepts `/v1/...`, `/chat/completions`, or `/responses`. The UI
   and its API live on the same listener but under other paths.
2. The `/v1` mount is stripped to build the upstream path; the query string is
   preserved. The request body is read once under `-max-body-mb`.
3. A record is created in `in_flight` state, and its id is returned to the
   client in `X-Cake-Proxy-Exchange` even when the upstream later fails.
4. Headers are copied except hop-by-hop headers, `Host`, `Content-Length`, and
   `Accept-Encoding`. Compression is disabled so captures are plain text.
5. If `-upstream-key-env` is set, the client credential is replaced in the
   configured header. `Authorization` is always removed first; an
   `Authorization` replacement is prefixed with `Bearer `, other headers get
   the raw value.
6. The upstream response status and headers are copied back. SSE responses
   have `Content-Length` removed.
7. Bytes are relayed as they arrive. SSE chunks are fed to an incremental
   parser and flushed immediately. The relay keeps a capped copy of the body,
   plus capped parsed events.
8. On completion the record is summarized (model, usage, finish reason, output
   text), marked `complete`, `canceled`, or `error`, and written to disk.

## Credentials

Two modes:

- no `-upstream-key-env`: forward the client's headers untouched
- `-upstream-key-env NAME`: substitute `$NAME` for the configured header

Cake requires a non-empty `api_key_env`, so the client sends a placeholder in
the substitution mode. The upstream key lives only in the proxy process. The
store redacts `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`,
`X-Api-Key`, `Api-Key`, `Openai-Api-Key`, `X-Auth-Token`, `X-Goog-Api-Key`, and
`Anthropic-Api-Key`. Sensitive query parameters (`api_key`, `apikey`, `key`,
`token`, `access_token`, `authorization`) are stored as `redacted`. Request and
response bodies are stored verbatim; the UI and API are loopback-only.

## Streaming

The relay loop is deliberately simple: `Read` → append to the bounded capture
buffer → feed the SSE parser when the content type is `text/event-stream` →
`Write` → `Flush`. Client write errors stop the relay and mark the record
`canceled`. A read error after the status is sent is recorded but cannot change
the response.

Some providers label an SSE body as `application/json`. If a non-SSE capture
starts with `data:` or `event:`, the proxy parses it as SSE after the fact,
stores the events, and marks the record as streamed. This mirrors Cake's own
fallback body sniffing.

## Record schema

One JSON file per finished exchange at `<data-dir>/exchanges/<id>.json`. The
top-level fields:

| Field | Meaning |
| --- | --- |
| `id` | UTC timestamp plus a random suffix; also the file name |
| `started_at`, `completed_at`, `duration_ms`, `ttfb_ms` | timing |
| `state` | `in_flight`, `complete`, `canceled`, `error`, `interrupted` |
| `method`, `path`, `query`, `upstream_url` | routing |
| `api` | `chat_completions`, `responses`, or `other` |
| `status`, `stream` | upstream outcome |
| `request_headers`, `response_headers` | redacted header maps |
| `request_body`, `response_body` | `{json}` when the payload is JSON, else `{text}` |
| `events` | parsed stream events, `{name,data}` each |
| `events_truncated`, `body_truncated` | capture caps hit |
| `model`, `usage`, `finish_reason`, `output_text` | best-effort summary |
| `error` | proxy or relay failure text |

`GET /api/exchanges` returns list summaries newest first.
`GET /api/exchanges/{id}` returns the full record. `DELETE /api/exchanges`
clears memory and disk. `GET /api/config` describes the running instance.

## Storage and retention

The default data root is `$XDG_DATA_HOME/cake-proxy`, else
`~/.local/share/cake-proxy`; `$CAKE_PROXY_DATA_DIR` and `-data-dir` override it.
`-data-dir ""` keeps records in memory only.

Files are written atomically with mode `0600` into a `0700` directory. The
newest `-keep` finished exchanges are retained (`0` keeps all, default 200).
In-flight records are never pruned. A record left `in_flight` by a crash is
loaded as `interrupted` and kept until retention removes it.

Event capture stops at 20,000 events per exchange and body capture stops at
`-max-body-mb`; both are marked in the record. Relaying never stops at those
caps.

## UI

The UI is embedded in the binary (`internal/web/static`) and needs no build
step or network access. It polls `/api/exchanges` every 1.2 seconds, refreshes
the selected record while it is in flight, and renders request messages, tool
calls, responses, raw JSON, stream events, and headers. All provider content is
rendered with `textContent`, never as HTML. Tab selection, scroll position, and
open event rows are preserved across refreshes.

## Testing

- `internal/sse`: chunk boundaries, CRLF, multi-line data, comments, flush.
- `internal/openai`: sample bodies and event sequences for both APIs.
- `internal/capture`: lifecycle, persistence, interrupted records, retention,
  redaction.
- `internal/proxy`: path mapping, header and credential handling, verbatim
  relay, incremental SSE delivery (the test blocks the upstream until the
  client has read the first event), error pass-through, oversize rejection.
- `internal/web`: index/assets, config, list/detail, clear, 404.
- `cmd/cake-proxy`: listen validation and public base formatting.
