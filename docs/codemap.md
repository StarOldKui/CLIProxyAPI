# CLIProxyAPI Codemap

## Maintenance Rule

- This codemap is the project architecture snapshot.
- Update it when system boundaries, runtime truth sources, persistence, routes, env contracts, auth flow, model routing, or core request flow changes.
- Keep this file compact and present-tense. Do not use it as a changelog or migration log.
- Add `docs/codemap-*.md` only when a subsystem needs detail that would bloat this file.

## Product Boundary

- Product: CLIProxyAPI, a Go proxy server exposing OpenAI, Gemini, Claude, Codex, and Amp-compatible APIs over local or hosted HTTP.
- Primary repo path: `/Users/alen/Shrimpfall-Goose/Code/CLIProxyAPI`.
- Module: `github.com/router-for-me/CLIProxyAPI/v6`.
- Server entrypoint: `cmd/server/main.go`.
- Embeddable SDK entrypoint: `sdk/cliproxy`.
- External model catalog source: embedded `internal/registry/models/models.json`, with optional remote refresh from `router-for-me/models` and `models.router-for.me`.
- Management panel source lives in `web/management`; the built single-file panel is embedded from `internal/managementasset/static/management.html`.

## What This Project Owns

- CLI flags and service startup for login, TUI, standalone server, cloud standby, and proxy serving.
- Gin HTTP API server, shared TCP protocol multiplexing, route registration, request auth, CORS, request logging, management routes, OAuth callbacks, Amp routes, and WebSocket relay attachment.
- Config loading, validation, comment-preserving config writes, hot reload, auth file watching, and auth synthesis.
- HTTP/TLS serving, Redis RESP usage queue access, optional pprof serving, request logging, log retention, streaming keep-alives, streaming bootstrap retries, and filtered upstream header passthrough.
- Token/config persistence through local files, Postgres, git, or S3-compatible object storage with local mirrors.
- Built-in management panel source, single-file panel build, and embedded `/management.html` serving.
- OAuth quota snapshots for the management panel, including server-side refresh, per-auth quota grouping data, and Codex subscription expiry derived from ID-token claims.
- Runtime auth manager integration, credential selection, retry/cooldown behavior, session affinity, model registration, and provider executor binding.
- Provider executors for Gemini, Vertex, Gemini CLI, AI Studio relay, Antigravity, Claude, Codex HTTP/WebSocket, Kimi, and OpenAI-compatible providers.
- Protocol translation registration and request/response conversion between OpenAI, Gemini, Claude, Codex, and related formats.
- Thinking normalization through `internal/thinking`: suffix parsing, canonical `ThinkingConfig`, validation/conversion, and provider-specific apply logic.
- Codex Responses request shaping, including automatic `image_generation` tool injection where supported by global config, the base model, and selected auth.
- Antigravity Google One AI credits fallback orchestration for Claude models when normal auth selection or execution reports quota exhaustion or unavailability.
- Usage record publication, persistent management usage statistics, request logging, Redis-compatible short-lived usage queue access, per-auth API-key request summaries, Antigravity signature cache, Codex prompt cache, and management debug/read APIs.

## What This Project Does Not Own

- Upstream provider APIs, provider account entitlements, billing, quota policy, and model availability outside the local registry snapshot.
- Secrets outside configured token stores and local mirrors.
- Vendor OAuth consent screens, callback authorization behavior, and provider token refresh semantics.

## Runtime Sources Of Truth

- Config path selection order: `PGSTORE_DSN` > `OBJECTSTORE_ENDPOINT` > `GITSTORE_GIT_URL` > `--config` > `$PWD/config.yaml`.
- `.env` is loaded from the working directory before env-backed store selection.
- YAML config is parsed by `internal/config.LoadConfigOptional` or `LoadConfig`; the struct in `internal/config/config.go` and `internal/config/sdk_config.go` defines the runtime schema and defaults.
- `DEPLOY=cloud` makes config optional. Missing, empty, or invalid config enters standby and does not start the API server.
- `remote-management.secret-key` may be bcrypt-hashed and written back during config load when plaintext is provided.
- Request access keys come from config `api-keys` through `internal/access/config_access` and `sdk/access`.
- Runtime auth records are managed by `sdk/cliproxy/auth.Manager`; auth files and API key config entries are synthesized into `coreauth.Auth` records by `internal/watcher/synthesizer`.
- Model routing uses `internal/registry.GetGlobalRegistry()` plus static catalog lookup. Auth registration populates provider/model availability.
- Codex tier model lists are loaded from `internal/registry/models/models.json` and augmented by `internal/registry.WithCodexBuiltins`, including hard-coded Codex built-ins such as `gpt-image-2`; static tier entries include `codex-auto-review`.
- `openai-compatibility[].disabled` removes a provider from auth synthesis, alias routing, proxy lookup, executor config resolution, and client counts while preserving the config entry for management editing.
- `disable-image-generation` is a runtime config gate. When enabled, OpenAI image endpoints return 404, Codex executors skip built-in image tool injection, and payload helpers remove `image_generation` tools from request tool arrays.
- `--local-model` skips remote model updater startup. Embedded model definitions and runtime auth model registration remain active.

## Request Flow

- Client request enters Gin routes in `internal/api/server.go`.
- The server starts one TCP listener. `internal/api/protocol_multiplexer.go` routes HTTP and TLS-negotiated HTTP connections into the Gin HTTP server, and routes Redis RESP connections into `internal/api/redis_queue_protocol.go` when management routes are enabled.
- `/healthz` serves GET/HEAD health checks. `/` serves a small JSON endpoint summary. `/management.html` serves the embedded management panel when the control panel is enabled; `MANAGEMENT_STATIC_PATH` can override it with an existing local file, and a missing override falls back to the embedded panel.
- `/v1` routes serve OpenAI-compatible chat/completions, completions, image generations/edits, Claude messages/count_tokens, OpenAI Responses HTTP, OpenAI Responses WebSocket, Responses compact, and model listing.
- OpenAI Responses WebSocket handlers subscribe to Codex executor upstream disconnect notifications and close the downstream client connection when the pinned upstream WebSocket session is invalidated.
- `/backend-api/codex` mirrors the Codex Responses routes for Codex CLI `chatgpt_base_url` compatibility: GET/POST `/responses` and POST `/responses/compact`.
- `/v1beta` routes serve Gemini-compatible model listing and model actions.
- `/v1internal:method` serves Gemini CLI internal requests and is gated by `enable-gemini-cli-endpoint`, loopback `RemoteAddr`, and `Host=127.0.0.1`.
- `/v0/management` routes are registered when `remote-management.secret-key`, `MANAGEMENT_PASSWORD`, or a local TUI password exists, then gated by management middleware rather than normal API key auth.
- `/v0/management/api-key-usage` returns in-memory API-key auth success/failure totals and 20 recent 10-minute request buckets grouped by provider and `base_url|api_key`.
- `/v0/management/usage`, `/v0/management/usage/export`, and `/v0/management/usage/import` expose the persistent aggregated usage snapshot for the built-in management panel.
- `/v0/management/usage-queue?count=N` pops JSON usage records from the same in-memory queue exposed through the Redis RESP interface.
- `/v0/management/quota/refresh` refreshes quota snapshots for selected or all supported OAuth auth files through CPA-managed token handling; `/codex-quota/refresh` remains a legacy alias.
- `/v1/ws` is the WebSocket relay path attached by `Server.AttachWebsocketRoute`; it creates runtime-only `aistudio-*` providers through `internal/wsrelay`.
- Amp routes live in `internal/api/modules/amp` and include `/api/provider/:provider/...`, `/api/provider/google/v1beta1/*path`, `/api/{internal,user,auth,meta,ads,telemetry,threads,otel,tab}` proxy routes, and root web routes such as `/threads`, `/docs`, `/settings`, `/auth`, and RSS endpoints.
- Redis RESP usage queue access accepts `AUTH` with the management key and supports destructive `LPOP`/`RPOP` from the in-memory usage queue. Unauthenticated Redis commands return `NOAUTH`; management-disabled servers reject Redis protocol handling.
- Standard provider request path:
  `Gin route -> AuthMiddleware -> sdk/api handler -> BaseAPIHandler -> coreauth.Manager -> provider executor -> sdk/translator -> upstream provider -> translator -> HTTP/SSE/WebSocket response`.
- `BaseAPIHandler` passes cloned inbound HTTP headers into executor options so auth selection can use header-derived session affinity, including `X-Session-ID`, `Session_id`, `X-Amp-Thread-Id`, and `X-Client-Request-Id`.
- Executors translate the request into target provider format, apply `thinking.ApplyThinking`, apply payload/provider config, inject auth and headers, call upstream, translate responses back to the source format, and publish usage.
- `sdk/cliproxy/usage.Manager` dispatches usage records to registered plugins. The Redis queue plugin includes provider, upstream model, client-requested alias, endpoint, auth type, auth index, API key, request ID, latency, status, error message for failed attempts, and token breakdown in queued records.
- Built-in request access accepts configured `api-keys` from `Authorization`, `X-Goog-Api-Key`, `X-Api-Key`, query `key`, or query `auth_token`.
- `AuthMiddleware` allows requests through when no access providers are registered. This is compatibility behavior, not proof that an endpoint has no auth concerns.

## Persistence And Reload Flow

- Default file storage uses `sdk/auth.FileTokenStore` for `auth-dir/*.json`; config remains the selected YAML file.
- Postgres storage is enabled by `PGSTORE_DSN`; it mirrors config/auth under `<PGSTORE_LOCAL_PATH|WRITABLE_PATH|cwd>/pgstore/{config,auths}`.
- Object storage is enabled by `OBJECTSTORE_ENDPOINT`; it uses S3-compatible keys under `config/config.yaml`, `auths/...`, and `usage/usage.json`, mirrored under `<OBJECTSTORE_LOCAL_PATH|WRITABLE_PATH|cwd>/objectstore/{config,auths,usage}`.
- Git storage is enabled by `GITSTORE_GIT_URL`; it uses repo-local `config/config.yaml` and `auths/`, with changes committed and pushed by the store.
- Remote store startup bootstraps from `config.example.yaml` when needed and overrides `cfg.AuthDir` to the local mirror auth directory.
- Watcher monitors the selected config path and auth directory. Config reload uses debounce and hash checks; auth reload handles same-directory `.json` files.
- Watcher auth updates flow through `WithSkipPersist()` to avoid writing the same file event back into storage.
- Store implementations that expose `PersistConfig()` or `PersistAuthFiles()` are used by watcher/management changes to push local mirror updates to the remote backend.
- `sdk/cliproxy.Service.Run` wires usage collection for CLI and SDK entrypoints. `internal/usage` aggregates records, including per-request failed-attempt error messages, into an in-process snapshot, loads `usage/usage.json`, flushes dirty snapshots every 15 minutes plus explicit import/shutdown flushes, and writes the same snapshot to S3-compatible object storage at `usage/usage.json` when `OBJECTSTORE_ENDPOINT` is selected.
- Local Docker Compose maps `./usage` to `/CLIProxyAPI/usage`, matching the config/auth/log volume pattern so file-mode usage snapshots survive container recreation.
- Object-store usage snapshot restore compares `saved_at` with file/object modified times and keeps the newer local mirror when S3 still has an older snapshot; transient S3 read failures fall back to the local mirror when it is readable.
- Management quota refresh stores the latest per-auth snapshot under auth metadata key `quota` for supported providers including Codex, Claude, Antigravity, Gemini CLI, and Kimi; this follows the existing auth-file/store persistence path instead of introducing a separate quota database.
- Redis usage queue stores JSON records in process memory under `internal/redisqueue`; `redis-usage-queue-retention-seconds` controls retention with default `60` and max `3600`. Disabling management clears the queue, and `usage-statistics-enabled` gates both persistent usage aggregation and Redis queue enqueuing.
- `sdk/cliproxy/auth.Auth` keeps per-auth `Success`, `Failed`, and recent-request bucket counters in memory. These counters are preserved across runtime auth updates but are not serialized to auth JSON.

## Runtime Assets And Diagnostics

- TLS is controlled by `tls.enable`, `tls.cert`, and `tls.key`; the main server creates a shared TCP listener, wraps it with `tls.NewListener` when TLS is enabled, and serves HTTP through the mux listener.
- pprof is a separate optional HTTP server controlled by `pprof.enable` and `pprof.addr`, defaulting to `127.0.0.1:8316`, and is re-applied on hot reload.
- Management panel serving prefers the embedded single-file asset. `MANAGEMENT_STATIC_PATH` is only a local debug override when its resolved `management.html` exists; the external auto-updater runs only when the binary has no embedded panel.
- Management quota refresh starts only while management routes are enabled, stops with server shutdown or management disablement, runs every 5 minutes with concurrency 5, and exposes cached snapshots through `/auth-files`.
- The management credential center is implemented by the `/auth-files` route; `/quota` redirects there. It renders all credentials grouped by provider in the all view, groups Codex credentials by plan inside Codex views, can sort within groups by name, priority, or remaining quota, and silently refreshes auth file health plus persisted quota snapshots every 5 seconds while open.
- Log output uses stdout by default or rotating `main.log` when `logging-to-file` is enabled. Log directory resolution prefers `<WRITABLE_PATH>/logs`, then writable `./logs`, then `<auth-dir>/logs`.
- `request-log` controls detailed request logging except in `commercial-mode`, which skips high-overhead request logging middleware.
- Gin request logging appends `[credits]` when executor context marks an Antigravity request as using Google One AI credits.
- `streaming.keepalive-seconds`, `streaming.bootstrap-retries`, `nonstream-keepalive-interval`, and `passthrough-headers` are SDK handler contracts applied in `sdk/api/handlers`.

## Auth And OAuth Flow

- CLI login flags in `cmd/server/main.go` dispatch to `internal/cmd.Do*Login` flows.
- CLI OAuth uses `sdk/auth.Manager` and provider authenticators, then persists `coreauth.Auth` through the registered global token store.
- Management auth starts under `/v0/management/*-auth-url`; callback-backed providers use `/v0/management/oauth-callback` or provider callback routes.
- Anthropic, Codex, Gemini, and Antigravity management OAuth register a state, return an auth URL, write `.oauth-<provider>-<state>.oauth` callback files in `auth-dir`, exchange credentials in a background goroutine, and save auth JSON.
- Kimi management auth uses device flow through `/v0/management/kimi-auth-url`; it waits for device authorization and does not use callback files.
- Web UI OAuth requests may start temporary provider callback forwarders so vendor redirects land on the server callback routes.
- OAuth sessions use short TTLs; callback file writers validate state/path inputs before touching auth files.
- Management middleware accepts `Authorization: Bearer <key>` or `X-Management-Key`; `MANAGEMENT_PASSWORD` is runtime-only, allows remote management access, and is not written into YAML.
- `internal/api/handlers/management.Handler.AuthenticateManagementKey` is the shared management-key verifier for HTTP management routes and Redis RESP `AUTH`. Failed attempts are tracked by client IP and five failures ban the IP for 30 minutes.

## Runtime And Translation Boundaries

- `sdk/cliproxy/service.go` owns server startup, watcher startup, auth update queue, WebSocket gateway, model refresh callback, auto refresh, pprof, and graceful shutdown.
- `sdk/cliproxy/builder.go` wires defaults for token providers, API key providers, access manager, core auth manager, selector strategy, and server options.
- `sdk/cliproxy/auth/conductor.go` owns auth selection, round-robin/fill-first strategy, session affinity, retries, cooldown, quota state, and executor dispatch. Selection metadata can request free-tier Codex auth records to be skipped.
- `sdk/cliproxy/auth/antigravity_credits.go` owns Antigravity credits context flags and per-auth credits hints. Conductor-level fallback is enabled by `quota-exceeded.antigravity-credits` and only targets Antigravity Claude-model routes when normal selection or bootstrap execution reports exhaustion/unavailability.
- `internal/runtime/executor` contains provider executors and their tests. Shared executor support belongs under `internal/runtime/executor/helps`.
- `internal/runtime/executor/antigravity_executor.go` injects `enabledCreditTypes=["GOOGLE_ONE_AI"]` only when the conductor marks the context with Antigravity credits, updates credits balance hints from `loadCodeAssist`, and marks credits usage for logging.
- `internal/runtime/executor/codex_executor.go` injects a PNG `image_generation` tool for Codex Responses HTTP, streaming, and compact requests unless `disable-image-generation` is enabled, the base model suffix indicates Spark, or the selected Codex auth is free-tier; existing image tools are preserved when the global gate allows image generation.
- `internal/runtime/executor/helps/payload_helpers.go` applies per-model payload config and removes `image_generation` tools from root or nested request tool arrays when `disable-image-generation` is enabled.
- Codex image tool usage is parsed from `response.tool_usage.image_gen` and published as additional model usage, defaulting to `gpt-image-2` when the tool omits its own model.
- `internal/runtime/executor/helps/usage_helpers.go` builds `sdk/cliproxy/usage.Record` values, carries the client-requested model alias when routing resolves to a different upstream model, and accepts Gemini CLI usage metadata at `response.usageMetadata` or top-level `usageMetadata`, including thought and cached token fields.
- `internal/translator` owns format conversion registration and implementation. Claude/Codex translation preserves reasoning signatures through Codex `encrypted_content` and Claude `thinking.signature`, handles `response.incomplete`, maps Codex stop reasons and stop sequences to Claude fields, and preserves declared Claude web search tool names. Translation should not own provider capability validation or runtime auth selection.
- `internal/thinking` preserves the canonical path: suffix/body extraction -> canonical `ThinkingConfig` -> validation/conversion -> provider applier.
- Thinking suffixes override body fields. Unknown or user-defined model capabilities pass through for upstream validation where applicable.
- Timeouts are limited to credential acquisition and specific documented exceptions: Codex WebSocket liveness deadlines, wsrelay session deadlines, management `APICall`, and model catalog fetch.
- Per-auth `proxy-url` overrides the global `proxy-url`; `direct` and `none` explicitly bypass inherited environment proxies.
- Session affinity identifiers are extracted in priority order from Claude Code `metadata.user_id`, `X-Session-ID`, `Session_id`, `X-Amp-Thread-Id`, `X-Client-Request-Id`, non-Claude `metadata.user_id`, `conversation_id`, then stable message hashes.

## Environment Contract

- Store selection: `PGSTORE_DSN`, `OBJECTSTORE_ENDPOINT`, `GITSTORE_GIT_URL`.
- Postgres store: `PGSTORE_DSN`, `PGSTORE_SCHEMA`, `PGSTORE_LOCAL_PATH`.
- Git store: `GITSTORE_GIT_URL`, `GITSTORE_GIT_USERNAME`, `GITSTORE_GIT_TOKEN`, `GITSTORE_GIT_BRANCH`, `GITSTORE_LOCAL_PATH`.
- Object store: `OBJECTSTORE_ENDPOINT`, `OBJECTSTORE_BUCKET`, `OBJECTSTORE_ACCESS_KEY`, `OBJECTSTORE_SECRET_KEY`, `OBJECTSTORE_LOCAL_PATH`.
- Runtime placement and mode: `WRITABLE_PATH` or `writable_path`, `DEPLOY`.
- Management runtime secret and static asset override: `MANAGEMENT_PASSWORD`, `MANAGEMENT_STATIC_PATH`. `/management.html` serves the embedded panel unless the override resolves to an existing `management.html`.
- Amp upstream secret fallback: `AMP_API_KEY`, after `ampcode.upstream-api-key` and before `~/.local/share/amp/secrets.json`.
- Lowercase variants for store env keys are accepted by `cmd/server/main.go` for compatibility.

## Critical Invariants

- Do not add upstream LLM request timeouts after the upstream connection is established.
- Keep imports at the top and keep Go changes formatted with `gofmt`.
- Keep `internal/translator` changes tied to broader behavior changes unless repository permission rules are satisfied.
- Keep request bodies reusable when middleware, fallback handlers, proxying, and handlers all need to read them.
- Preserve delayed SSE header commit in streaming paths so early upstream errors return JSON status before SSE headers are written.
- Keep filtered upstream header passthrough opt-in; never forward hop-by-hop, cookie, content-length/content-encoding, or known AI gateway fingerprint headers.
- Keep management auth separate from normal API key auth.
- Keep Redis RESP and HTTP usage queue access gated by management availability and management-key authentication.
- Keep persistent `/v0/management/usage` separate from destructive `/v0/management/usage-queue`.
- Keep usage manager shutdown draining queued records before the final usage snapshot flush.
- Treat usage queue reads as destructive pops; external collectors must drain within `redis-usage-queue-retention-seconds`.
- Keep `/v1/responses` WebSocket behavior distinct from `/v1/ws` relay behavior.
- Keep `/backend-api/codex` as aliases over the same Responses handlers, not as a separate Codex execution path.
- Scrub local client auth, fingerprint, and proxy headers before Amp upstream proxying.
- Keep Antigravity credits fallback as conductor-owned last-resort behavior; the executor should only use credits when the context explicitly requests it.
- Keep `disable-image-generation` as a global gate: image endpoints return 404, executor auto-injection is skipped, and request payload `image_generation` tools are stripped.
- Keep Codex image tool injection conditional on global config, base model support, and selected auth capability, and avoid duplicating an existing client-provided `image_generation` tool.
- Keep image generation handlers on non-free Codex auth when they route through Codex-backed Responses execution and the global image gate allows it.
- Rebind executors and refresh scheduler state when config, auth, model catalog, or selector settings change.

## Child Codemaps

- None. Add child codemaps only when API routes, management/OAuth, Amp, wsrelay, or storage details need standalone detail.

## Read-First Files

- Startup and modes: `cmd/server/main.go`, `internal/cmd/run.go`.
- Config schema and defaults: `internal/config/config.go`, `internal/config/sdk_config.go`, `config.example.yaml`.
- Server and routes: `internal/api/server.go`, `internal/api/protocol_multiplexer.go`, `internal/api/redis_queue_protocol.go`, `sdk/api/handlers/handlers.go`, `sdk/api/handlers/openai/openai_handlers.go`, `sdk/api/handlers/openai/openai_responses_handlers.go`, `sdk/api/handlers/openai/openai_responses_websocket.go`, `sdk/api/handlers/openai/openai_images_handlers.go`, `sdk/api/handlers/gemini/gemini_handlers.go`, `sdk/api/handlers/claude/code_handlers.go`.
- Management and OAuth: `internal/api/handlers/management/handler.go`, `internal/api/handlers/management/auth_files.go`, `internal/api/handlers/management/codex_quota.go`, `internal/api/handlers/management/oauth_sessions.go`, `internal/api/handlers/management/oauth_callback.go`, `internal/api/handlers/management/usage.go`, `internal/api/handlers/management/api_key_usage.go`.
- Runtime assets and diagnostics: `internal/managementasset/embedded.go`, `internal/managementasset/updater.go`, `web/management`, `internal/logging/global_logger.go`, `internal/logging/gin_logger.go`, `internal/logging/requestmeta.go`, `sdk/cliproxy/pprof_server.go`.
- Amp: `internal/api/modules/amp/routes.go`, `internal/api/modules/amp/fallback_handlers.go`, `internal/api/modules/amp/proxy.go`.
- SDK service: `sdk/cliproxy/builder.go`, `sdk/cliproxy/service.go`, `sdk/cliproxy/auth/conductor.go`, `sdk/cliproxy/auth/antigravity_credits.go`.
- Watcher and synthesis: `internal/watcher/watcher.go`, `internal/watcher/config_reload.go`, `internal/watcher/dispatcher.go`, `internal/watcher/synthesizer/config.go`, `internal/watcher/synthesizer/file.go`.
- Stores: `sdk/auth/filestore.go`, `internal/store/postgresstore.go`, `internal/store/objectstore.go`, `internal/store/gitstore.go`.
- Runtime execution: `internal/runtime/executor/*.go`, `internal/runtime/executor/helps/*.go`.
- Translation and thinking: `sdk/translator/registry.go`, `sdk/translator/pipeline.go`, `internal/translator/init.go`, `internal/thinking/apply.go`, `internal/thinking/validate.go`, `internal/thinking/convert.go`.
- Model registry and usage: `internal/registry/model_definitions.go`, `internal/registry/model_registry.go`, `internal/registry/model_updater.go`, `sdk/cliproxy/usage/manager.go`, `internal/usage`, `internal/redisqueue`.
- WebSocket relay: `internal/wsrelay/manager.go`, `internal/wsrelay/http.go`, `internal/wsrelay/session.go`.
