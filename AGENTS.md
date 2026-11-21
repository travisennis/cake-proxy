# Agent Instructions

## Project

`cake-proxy` is a single-binary Go HTTP proxy for the OpenAI Chat Completions
and Responses APIs. It forwards every request to one configured upstream,
relays streaming responses unchanged, and records each exchange for an embedded
HTML inspector. It exists so a Cake run can be inspected end to end without
reading provider payloads out of Cake's session files or turning on verbose
logs.

## Boundaries

- **Transparent relay.** Do not rewrite request or response bodies, statuses,
  headers, or stream framing. The only deliberate rewrite is credential
  substitution when `-upstream-key-env` is set.
- **Never buffer a stream.** Read a chunk, write it, flush it. The inspector may
  observe bytes but must not delay them.
- **Credentials never persist.** Store credential headers as `<redacted>`,
  never log the upstream key, and never print request bodies at startup. Bodies
  are otherwise verbatim and are sensitive.
- **Local by default.** Refuse a non-loopback listen without `-allow-remote`.
  The inspector has no authentication; do not add a remote mode without adding
  real auth.
- **Bounded capture.** Body capture and event capture have hard caps, and every
  truncation is visible in the record and UI.
- **One upstream per process.** Multiple providers mean multiple proxy
  processes.
- **Unknown shapes pass through.** Analysis of usage, model, and output text is
  best effort. A provider payload that does not match either API must still be
  relayed and stored.
- Tests must not require a network call, a provider credential, or Cake.

## Compatibility surfaces

Read [docs/design.md](docs/design.md) before changing any of these:

- the record JSON schema in `internal/capture`
- the `/v1` path mapping and header forwarding rules
- the streaming relay loop and SSE parsing
- the credential substitution rules
- the inspector's JSON API

## Commands

`justfile` is the authority:

```sh
just hooks
just test
just test-race
just ci
just build
just install
```

## Workflow

- Conventional Commits; `just hooks` installs the commit-msg hook.
- `ahm` owns the backlog. Run `ahm init` only after the repository has a remote,
  so the store keys on remote identity rather than a path hash.
- Do not keep a `TODO.md`. Future work belongs in ahm tasks.
- Keep `README.md`, the startup usage text, and `docs/design.md` in sync when
  flags, endpoints, the record shape, or guarantees change.
