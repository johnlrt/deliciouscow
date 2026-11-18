# Cordova — backend and Rust node agent plan

**Goal:** Implement the Go control plane and Rust inference node with independently verified usage, atomic accounting and conservative XMR operations.
**Date:** 2026-10-08 (Asia/Baku). **Version:** v1. **Owner:** backend agent. **Status:** implementation queue; application code does not yet exist.

Apply planiuer Core (services and phases), Security (untrusted nodes/custody), Quality (Go/Rust boundaries), and First-Run (external contracts and crash tests). Skip none. Read [PLAN.md](PLAN.md) first. It exclusively owns shared types, endpoints, WS schemas, model catalog, token accounting, ledger/routing/rank rules and financial defaults. This plan owns packages, node crates and implementation/verification tasks; consume shared contracts instead of redefining them.

## Summary

Implement small Go commands sharing a domain layer: gateway, tokenizer, event publisher, ranker, projections and payments.
Implement the Rust node as safe orchestration around a pinned llama.cpp subprocess.
Start with contract/money/wallet/tokenizer spikes and a complete mock-node path.
Develop real-node inference and ledger recovery in parallel after that slice.
Keep financial state in Redis and ranking events in Kafka; keep request content in volatile memory.
Stop a release on uncertain accounting, undetected replay, privacy sentinel leakage or duplicate payout.

## Core 1 — Service responsibilities

| Module/service | Responsibility | Inputs/outputs owned by PLAN |
| --- | --- | --- |
| Gateway | Authenticate HTTP, render API errors/SSE and manage browser/node sockets | C1–C4 |
| Billing domain | Reserve, settle, refund and mature earnings through invariant-checked operations | C5/C7 |
| Router | Match catalog capability and claim session-bound capacity leases | C3/C6 |
| Tokenizer helper | Apply approved templates and count input/output using the pinned trusted artifact | C2 token accounting; C9 |
| Event publisher | Forward durable Redis outbox metadata to Kafka with stable event IDs | C7 |
| Ranker | Project benchmarks/outcomes into versioned rank/capacity/fraud state | C6/C7; threat model |
| Account projections | Maintain usage/analytics and owner-only live invalidations | C1/C4/C7 |
| Payments | Allocate subaddresses, observe deposits, prepare/relay payouts and reconcile | C8 |
| Rust node | Maintain authenticated outbound WS, approved models, local inference and cleanup | C3; privacy/security sections |

Keep gateway request billing inside the same Go process as request orchestration, with a narrow billing interface; payment/rank/event workers run separately for credential/network isolation. No worker accepts financial instructions from Kafka. Node inference gets only the approved model, tokenized content, limits and lease metadata. Do not expose llama.cpp endpoints through the public gateway.

## Core 2 — Layout and dependency direction

Use the stack/version policy from [PLAN Core 2](PLAN.md#core-2--stack-and-architecture). Prefer `net/http`, standard structured logging with a field allowlist, a maintained Redis Go client, franz-go, a checked password/TOTP library and a verified schema generator. Use Tokio, tokio-tungstenite, rustls, reqwest, a maintained SOCKS5 connector and OS credential storage in Rust. Pin and justify each dependency; direct Rust FFI and application `unsafe` are unnecessary.

```text
contracts/
  openapi.yaml, ws/v1.schema.json, internal/tokenizer.schema.json
  fixtures/{rest,node-ws,live-ws,tokenizer}/
  generated/{go,rust,typescript}/
  versions.lock.json
server/
  go.mod, go.sum
  cmd/{gateway,tokenizer,event-publisher,ranker,projections,payments,migrate,mock-node}/main.go
  internal/
    config/                 # Role-specific validated startup config and secret references
    contracts/              # Generated bindings, validators and error-code helpers
    auth/                   # Sessions/password/TOTP/API-key verifiers and scope checks
    enrollment/             # Link approval, HWID signal, nonce/proof and signed node tokens
    gateway/                # HTTP endpoints, ownership middleware, error/SSE presentation
    node_session/           # WSS ownership, sequence validation, heartbeat and fencing
    request/                # Logical request/attempt orchestration and cancellation
    catalog/                # Manifest signing/verification and immutable price snapshots
    tokenizer/              # Trusted helper adapter and local process supervision
    router/                 # Candidate tiers, queue deadlines and lease adapter
    billing/                # Money/postings/state machine and idempotent financial commands
    ranking/                # Pure rank formula, capacity challenges and fraud signals
    payments/               # Deposit normalization, payout machine and reconciliation
    projections/            # Account usage/analytics/live invalidation consumers
    events/                 # Outbox/Kafka typed adapters and replay-safe consumption
    storage/redis/          # Key builders, repositories, connection/fsync and migrations
    storage/redis/lua/      # Narrow atomic scripts and decimal-string arithmetic helpers
    observability/          # Approved fields, metrics and safe tracing
    testkit/                # Fake clocks/IDs/RPC/nodes and deterministic seeds
  tests/integration/        # Real Redis/Kafka and simulated wallet/node faults
node/
  Cargo.toml, Cargo.lock, rust-toolchain.toml
  crates/
    cordova-node/           # CLI login/run/status/logout/config and process lifecycle
    protocol/              # Generated envelopes plus strict validation/state transitions
    ws-client/             # Auth challenge, TLS, heartbeat, reconnect and session fencing
    inference/             # Safe model-runner interface and llama.cpp subprocess adapter
    model-manager/         # Signed catalog, hash checking, download/load/cache eviction
    hardware/              # Portable probe and reported hardware/HWID normalization
    proxy/                 # SOCKS5h TCP/TLS connector and proxy credential redaction
    credentials/           # OS keyring/fallback, migration and rotation
  tests/{mock_server,privacy,model_runner,platform}/
  fuzz/fuzz_targets/        # Envelope parser/state machine fuzzing
tests/system/               # Cross-service API/billing/node/payment/load scenarios
docs/
  verification.md, dependencies.md, node-setup.md, backend-runbook.md
```

Domain money/state/rank functions depend on types and injected clocks/IDs, not Redis, HTTP, Kafka or wallet RPC. Adapters depend on domain interfaces. Keep generated types isolated from authored validation logic; rejection rules are testable adapters, not silently permissive deserialization. The mock node speaks the same generated protocol as the real node.

## Core 3 — Dependency-ordered implementation tasks

Create commands below as part of implementation; they are planned acceptance checks, not verified existing commands. M numbers link to [shared milestones](PLAN.md#core-3--dependency-ordered-milestones).

### B0 — Verify foundations and publish contracts (M0; first; M)

Generate OpenAPI/JSON schemas and complete positive/negative fixture corpora for every shared REST DTO, message variant and error. Select generators that actually support the pinned OpenAPI 3.1/JSON Schema drafts; smoke-check discriminator/nullable/int-string handling before committing a tool. Use a small deterministic custom generator only for uncovered domain bindings, with generation/fixture tests; never silently downgrade nullability or money types. Publish frontend types early. Record exact tool and upstream source versions, real sample outputs and license evidence without live credentials or user content.

Run spikes for Redis Lua decimal operations above 2^53 and near int64 limits, idempotency, wrong key types/runtime errors, connection-scoped fsync barriers, Kafka outbox duplication, model template/BOS/token parity, slot cleanup, SOCKS5 remote DNS and stagenet payout prepare/relay ambiguity. Build the selected model from its licensed upstream weights into a reproducible GGUF, compute artifact/tokenizer/template hashes and sign a test manifest. Reject an unsupported model rather than approximate token counts. Integrate these spikes into M0, before production domain code.

**Done when:** `make contracts-check && make verify-foundations` passes and `docs/verification.md` contains pinned real samples and observed failure behavior for every external integration, including missing/unsupported cases that fail closed.

### B1 — Build Go domain, configuration and auth (M1; B0; M)

Implement role-specific config validation, normalized account IDs/usernames, checked money types, explicit request/attempt/lease enums and interfaces for store, tokenizer, node runner and wallet. Bootstrap schema version with `migrate` using an audited operator path. Implement auth/enrollment endpoints from C2/C3 with constant-time verifiers, Argon2id, server-side sessions, CSRF, recovery-code consumption, TOTP setup/step-up, key scopes/caps and encrypted secret records. Choose an Argon2id cost through an M0 hardware benchmark targeting roughly 250ms verification with concurrency limits; record parameters in each hash and allow upgrades on login.

Issue node secret only after owner link; bind the pending enrollment/device secret and reject double linking. Handle lost enrollment-token response using the C2 bounded encrypted re-read. Revoke key/session/node immediately and fence their active work. Validate public model names/account ownership at every handler. Avoid a privileged admin HTTP endpoint; operator changes use audited internal CLI operations with appropriate ACLs.

**Done when:** `cd server && go test -race ./internal/auth/... ./internal/enrollment/... ./internal/config/... ./internal/billing/...` passes theft/replay/expiry/CSRF/tenant/cap fixtures; startup refuses absent secrets, invalid network or incompatible schema version.

### B2 — Build gateway, sockets and mock-node vertical slice (M1; B1; parallel with frontend/infra; M)

Implement C2 HTTP/OpenAI routes, C3 node sockets and C4 live sockets with separate auth/type registries. Use one read pump and one bounded write pump per connection; context-cancel all goroutines on disconnect. Track session generations in Redis and local maps; reject old-session frames before invoking request logic. Restrict origin on browser sockets, use signed node Authorization on node sockets and disable frame compression. Implement strict duplicate-key/unknown-field/enum/size parsers with safe error mapping.

Implement mock node modes for healthy, unavailable, slow accept, stalled output, dropped connection, invalid count, invalid model hash, duplicate chunks and malformed frames. Wire a full synthetic/test-credit request through nginx, Redis outbox, Kafka projection and dashboard invalidation. For SSE, use a streaming-capable response writer, heartbeat while idle and bounded per-request queues; flush failures cancel the attempt. Apply write timeouts per stream flush, not one global HTTP timeout that kills valid 120s responses.

**Done when:** `make smoke && make test-protocol` passes the full mock-node slice; malformed/unauthenticated/cross-account sockets fail, no goroutines leak after repeated disconnects and SSE chunks arrive before inference completes.

### B3 — Implement trusted tokenization (M2; B0/B2; S)

Implement C9 helper and client. Run the same pinned llama.cpp/artifact/template version as admitted nodes on the private tokenizer service. Convert incoming supported messages into canonical token IDs; forward IDs to node `/completion` so nodes cannot choose a different template. Apply output whole-text counting before forwarding every chunk and at terminal settlement. Disable hidden reasoning/tool behavior through admitted model/template settings; reject context overflow and unsupported generation options before reservation.

Use bounded tokenizer concurrency/queue/deadline and fail requests with `tokenizer_unavailable` if authoritative counts cannot be produced; never fall back to node counts. Refund an active request on this infrastructure failure without penalizing its node, and do not retry another node while the shared tokenizer is unavailable. Keep prompt/text/token arrays in request-owned memory, release them on all branches and prevent helper logs/slot caches from retaining content. Test chunk-boundary BPE changes, multi-byte UTF-8, control-token-looking input, empty output, stops/EOS, long turns and exactly-at-limit output. Do not count each chunk separately.

**Done when:** `make test-tokenizer-parity` compares actual helper/node prompt token IDs and canonical output counts for all admitted models; shuffled chunk boundaries yield identical final usage and invalid/oversize text is never forwarded.

### B4 — Build Rust CLI, credentials, transport and proxy (M2; B0/B2; parallel with B3/B7; M)

Implement `cordova-node login`, `run`, `status`, `logout` and `config`. `login` prints/opens the verification URI and waits for owner linking; persist only secret/config/HWID salt, never content. Make `login` install/enable the user-level automatic service when platform support exists; foreground `run` remains supported. No mandatory CLI flags after login: detect hardware, fetch signed catalog, reconnect and manage models automatically. `status` reports connection/model/active-slot state without secrets; `logout` drains/revokes local credentials and provides server-revoke guidance.

Support `--server` only for first enrollment, `--proxy socks5h://host:port`, `--model-dir`, `--max-disk-gib`, `--max-concurrency`, `--foreground` and `--log-level`; config/environment carry equivalent non-secret overrides. Default model cache cap 20GiB, one download, one calibrated inference slot until benchmark, with automatic headroom validation. Proxy credentials use credential storage, not status output. Resolve both server and model-host names through proxy in strict mode; fail closed instead of direct fallback. Preserve TLS hostname/certificate validation after SOCKS tunneling and verify trusted-server signing keys independent of proxy.

Implement C3's auth/heartbeat/backoff, bounded channels and reconnect session invalidation. Do not replay inference after reconnect. Use monotonic local deadlines; reject expired dispatches. On Ctrl-C/OS shutdown, send goodbye/drain, finish within the shared grace period, cancel child work, erase slots and kill the entire child process group. Test token expiry, machine clock skew, TLS failure, Tor-like latency, proxy disconnect and OS credential-store denial with a clear actionable local error.

**Done when:** `cd node && cargo test --workspace` plus `make test-node-transport` proves zero-config login/run/reconnect, revoked/stale auth failure and no direct network/DNS traffic in SOCKS5h fail-closed tests; Linux/Windows/macOS transport smoke passes.

### B5 — Implement model manager and llama.cpp runner (M2; B3/B4; M)

Manage one runner process per loaded model with explicitly assigned slots. Spawn the bundled verified executable using argument arrays, never a shell. Bind its HTTP API to randomized loopback port with a per-process secret; discard stdout/stderr or route only independently constructed safe status events, never native strings. Verify model-manager downloads against signed manifest URL/host allowlist, size and hashes; block file/private-address redirects, path traversal, symlinks out of cache and unsigned catalog changes. Resume verified-range downloads into temporary artifact files, then atomic rename after full hash; downloads are model weights only, not request data. Check disk/memory headroom before download/load, retain rollback artifact version and evict only idle/unleased models.

Use explicit concurrency calibrated by server benchmark and local safety caps. Feed exact token IDs from C3, request streaming and map only safe completion fields into typed deltas/completion. Set `cache_prompt:false`, disable RAM/idle prompt-cache reuse and never enable a slot-save path or prompt logging. On finish/cancel/error erase the assigned slot before making it available; if cleanup is unsupported/fails, kill and reload that child before reuse. Verify cancellation actually stops computation. Context arrays/buffers must not remain reachable beyond request cleanup. This removes supported logical cache retention; it does not guarantee forensic zeroization of allocator/GPU memory or constrain a malicious owner. The pinned server documents token-ID prompts, tokenizer/template endpoints and slot erasure; re-verify those behaviors at M0. [llama.cpp server interface](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md).

Probe GPU identity/memory through supported safe APIs or vetted platform commands without shell interpolation: Linux/Windows NVIDIA tooling where present, macOS Metal/unified-memory report, CPU/RAM fallback. Label raw probe data as reported; do not fabricate a VRAM number for unified-memory/CPU devices. Preserve actual platform-specific capacity interpretation in the benchmark result. A failed inference child quarantines the local model until it is reloaded and rechecked.

**Done when:** `make test-model-runner && make test-node-privacy` passes actual generation, download tampering/resume/disk exhaustion, process crash, OOM, cancellation and slot erasure; active requests never lose their model to background eviction and no sentinel prompt/output reaches logs or files.

### B6 — Implement routing, queueing and capacity challenges (M2; B2/B5; M)

Implement the exact C6 tier ordering and handshake; no alternate shortcut routes. Redis Lua slot/load lease claim validates current session, verified model version, effective concurrency and deadline in one operation. Reaper releases leases explicitly and sends cancellation when possible; timeout cleanup is idempotent. Separate routing availability from rank projections, which may lag. Bound catalog lookups, query fanout and queued work; do not stall one ready node while waiting for an unrelated download.

Calibrate concurrency/memory with server-generated benchmark challenges, using server token counts/timing and success outcomes. Persist demonstrated lower bounds and slot limits only after all required concurrent slots succeed. Hardware reports cannot raise those limits. Track benchmark leases separately but subtract them from inference availability. Return exact C2 warming/capacity/not-found errors with no reservation leak. Node rejection or stale state triggers eligible retry within the existing request deadline and A09 rules.

**Done when:** `make test-node-routing` covers each routing tier, busy-with-spare versus saturated, load deadline, background download not serving current request, unavailable capability, concurrent claims and old-session completions; peak leased slots never exceed calibrated capacity.

### B7 — Implement billing, lifecycle and recovery (M3; B1/B2; parallel with B3–B6 against mock node; L)

Implement C5 scripts and request orchestrator. Use decimal-string Lua arithmetic rather than `tonumber` for money; prevalidate every script argument/key type/state transition before mutation. Verify expected journal head/revision before appending the next canonical hash-chain entry; recompute/retry on a competing writer. Keep balance/postings/cap counters/operation tombstone/outbox updates in one script. Reserve before dispatch and durable-ack settle before success. Use a dedicated Redis connection through the Lua write and fsync barrier; connection pooling must not accidentally wait on another client's writes. Retry uncertain writes only with their original operation ID.

Snapshot prices, checked rounding and owner share; write table-driven and property tests of every posting kind using an independent reference model. Implement attempt refund/re-reservation with remaining cap/funds checks, one retry on a distinct owner and no streaming retry after content. Implement client cancel prefix settlement, key revoke, malicious oversize/count frames, late completion races and server shutdown. Apply reservations to the acceptance day's cap bucket, release that same bucket on failure, and keep accrued settlement in the documented day bucket even across UTC midnight. No output means cancelled charge zero; complete nonstreamed inference remains billable even if the client fails to read its final response after durable settlement.

Implement startup recovery with readiness held false: fence node sessions, enumerate active metadata (never missing prompt bodies), inspect operation tombstones and refund only orphan reservations, skip already-settled operations, reconcile cap counters/leases, then reopen. Avoid TTL expiration of financial request/lease records as a substitute for recovery. Keep metadata-only idempotency responses per C2. Trip billing/inference circuits on balance/ledger/fsync uncertainty and surface request status `recovering` without implying rollback.

**Done when:** `make test-ledger-recovery` runs concurrent spends/caps/settle-timeout/cancel plus crash injection at every financial boundary and passes independent journal replay: conserved postings, no negative user buckets, one terminal outcome, zero unreaped reservations and no float precision loss.

### B8 — Implement outbox, rank and fraud pipeline (M4A; B6/B7; parallel with payments; M)

Produce C7 events from validated server observations; never publish raw frames or prompts. Forward durable events with stable event IDs and keyed ordering. Update Redis projection plus consumer offset atomically, then acknowledge Kafka. Dedupe repeated outbox deliveries and reject older session/sequence facts; use observed time for heartbeat/latency. Route poison events into metadata-only DLQ so a bad record does not stall all nodes. Build deterministic versioned rank recomputation and account usage projections; close offline routes immediately without waiting for consumer lag.

Run first-link/daily capacity challenges and randomized property canaries. Select A11 sampling at request dispatch while prompt is still in the active request memory scope; sample a different owner and prohibit the shadow from becoming a user response. Compare outputs before logical-request cleanup or discard/cancel unfinished shadow work at terminal time; never retain text to perform checks afterward. Fund verified shadow work using A19, measure sampling shortfall and exercise the budget in tests. No sample capacity means metrics plus continued canaries, not duplicate user charges.

Implement T01–T05 count/timing/quality signals, corroboration and audited hold/release controls. Apply rank zero/quarantine immediately for protocol fraud; schedule no payout while a hold is active. A plausible-output disagreement is a review signal, not automatic proof of theft; maintain test fixtures for legitimate stochastic divergence, batching and Tor latency. Published penalty policy and operator records explain withheld earnings; no raw sampled text appears in evidence storage.

**Done when:** `make test-rank-fraud` proves rank formula fixtures, event replay/out-of-order safety, foreign-owner sampling, platform-funded verification, canary detection, false-positive release and held earnings excluded from payout eligibility.

### B9 — Implement deposits, payout state machine and reconciliation (M4B; B7 + B0 wallet samples; parallel with B8; L)

Implement C8 behind typed wallet RPC interfaces with digest authentication and bounded deadlines. Use a single spending writer process with an OS exclusivity lock and supervised lifecycle; Redis leader/epoch checks assist coordination but wallet RPC cannot validate a Redis fencing token. Forbid automatic replacement while an old writer or RPC request may still act. Only this worker receives wallet credentials/network access; the gateway receives deposit/payout projections from Redis, never direct wallet access.

Implement stable subaddress allocation/recovery, cumulative per-tx/subaddress receipts, confirmation/spendability checks, cursor overlap/full recheck and generation-based credit/reorg reversals. Test two users paid by the same transaction, several outputs to one subaddress, duplicate scan windows, locked deposits, daemon lag and reorg below confirmations. Account deficit handling freezes spend/payout, books platform loss/receivable through an explicit audited operation and never manufactures spendable credit.

Implement maturity and hold checks atomically at payout reservation. Validate configured network addresses with wallet validation and TOTP/cooldown metadata. Prepare batched transfers, persist/ack exact signed metadata/destination mappings/fees/hashes and relay only persisted material. Test split transaction partial relay and ambiguous prepare/relay; leave unknown batches reserved until proven resolved. Do not emit “paid” on mere RPC invocation. Reconciliation checks liability backing and payout liquidity separately; include fee expense, pending chain cash and operator cold treasury attestations. Emergency payout stop can interrupt future relay without reconstructing batches.

**Done when:** `make test-payments-stagenet` and `make test-payment-faults` prove one credit per observed delta, ten-confirmation behavior, reorg handling, one signed payout material set despite timeout/restart, split partial-broadcast recovery and zero unexplained reconciliation difference; hot/cold runbook entries are auditable.

### B10 — Implement observability and privacy proof (M2–M5; start after B2; M)

Use structured logs with allowlisted route template/status/safe error/request ID/duration fields; strip query strings, headers, usernames, payment addresses and native exception/HTTP bodies. Avoid metric labels that include prompts/account/key/request IDs. Export gateway requests/status, authoritative token totals, reservation age/uncertain writes, lease counts, node heartbeat/latency, tokenizer latency/queue, canary/sample flags, outbox/Kafka lag, deposit lag, payout-state counts and reconciliation difference. Record server-measured inference time rather than node timing claims. Keep auth and financial incident actions in the audit journal with opaque operator IDs.

Create high-entropy UTF-8 prompt/response sentinels and exercise success, failure, reconnect, cancellation, native crash and sampling. Capture process output/network/write observations in harness memory, inspect logs, temporary/home/cache directories, child write syscalls where available, Redis/Kafka payloads, traces and backups, and fail if sentinel content or reversible encodings are persisted. Reports contain pass/fail and safe metadata only; the harness must not itself save captured content. Test approved node releases under read-only root plus explicitly allowed model/config/credential paths. Disable core dumps and managed-host swap; document user-host OS limits. Verify saved slot files are absent and slots clear before reuse. Intentional secrets/model files are permitted writes; prompts/outputs are not.

**Done when:** `make test-privacy` passes through gateway/helper/node/all workers including error branches; metrics/log inspections match the inventory and no raw content enters Redis/Kafka or archived backups.

### B11 — Qualify load, platform behavior and security (M5/M6; B3–B10; M)

Run Go race tests, Rust checks, parser fuzzing and real Redis/Kafka integration before load. Use deterministic mock nodes for 100 sockets/20 simultaneous streams and admitted real-model nodes for measured GPU throughput. Soak one hour at the A01 pilot target, churn 10% sockets/minute, kill nodes/worker connections and throttle SSE readers. After warm-up, require no unbounded queue growth, no lost financial operation, no leaked slot/reservation and gateway memory growth <10% over the final 45min. With immediate mock-node output, routing p95 <100ms and first forwarded chunk overhead p95 <250ms including local tokenizer work; publish separate real-model first-token/tokens-per-second measurements. If the tokenizer budget fails, lower pilot capacity until measured optimization passes, without weakening counts.

Fuzz Go/Rust node/live parsers for at least 10 CPU-minutes each in PR CI and two CPU-hours scheduled daily; retain minimizing crash corpora, validate no parser panics/hangs/unbounded allocations and replay regressions in unit tests. Smoke supported OS builds with real llama.cpp CPU fallback; GPU-specific builds need actual GPU runner evidence. Verify subprocess cleanup/credentials/proxy paths on each platform. Conduct adversarial account/node replay/output/cap/payment tests and external review before mainnet.

**Done when:** `make test-backend && make load-pilot && make test-node-platforms && make test-adversarial` passes with archived content-safe results and measured environment specs; untested GPU combinations are labeled experimental and omitted from “verified” release claims.

## Core 4 — Assumptions

- Execute shared A01–A19 and D01–D11; benchmarked limits, not raw hardware claims, determine dispatch.
- Keep one active gateway session owner and one supervised wallet writer in v1. Do not add automatic multi-gateway failover or distributed inference replay.
- Use trusted tokenizer process service C9, full approved GGUF weights initially and explicit per-model readiness. Tokenizer-only weight extraction is a later optimization needing parity proof.
- Use CPU builds as portable fallback; optional CUDA/Metal/Vulkan releases may advertise only tested combinations. Unsupported driver/backend produces an actionable error, not a fabricated report.
- The supported llama.cpp slot erasure API removes logical caches; forensic allocator/GPU zeroization is outside the guarantee and must remain disclosed.
- M0/M1 tests use mock nodes and isolated test credits; only M4B uses stagenet. No task automatically spends mainnet money.
- File/module ownership is fixed above; infra owns Compose/CI/root orchestration and frontend owns browser specs. Supply your checks as callable scripts without editing another owner's implementation files concurrently.

## Core 5 — Decisions and caveats

Use [PLAN Decision log](PLAN.md#decision-log) as the only decision registry. The subprocess alternative wins over FFI because application memory safety/ABI portability and crash isolation are easier to verify. It does not make native llama.cpp memory-safe; contain and audit that boundary. Library API details, wallet metadata layouts and GPU probe behavior remain unverified until their explicit M0/M2 gates pass.

## Security and privacy execution

Implement the shared threat model through B1 (T05–T07/T14), B3/B8 (T01–T04), B5/B10 (T09/T10/T13), B7 (T11) and B9 (T08/T12). Use encrypted secret records with external key IDs, constant-time verifiers, no auth-value logging and safe public error messages. Apply bounds before allocation/deserialization and before passing data to native code. Validate event producers and source sessions rather than treating Kafka input as trusted merely because it is private.

CI gates and secret/dependency policy are owned by PLAN and infra; supply exact commands and reviewed exceptions, never suppress warnings to get green. Application crates use `#![forbid(unsafe_code)]`. Document native/dependency unsafe inventory and isolate its inputs/process privileges. The node cannot enforce privacy against its owner; distribute honest docs rather than an attestation claim. On incidents, independently stop dispatch/billing/payout circuits and preserve only approved metadata.

## Quality and test strategy

Keep pure money/posting/state/ranking tests table-driven, property-based and independent of implementation algorithms. Cover conservation, rounding around 1M tokens, one-piconero deltas, int64 overflow, capped output, failed reserve/refund and append-only reversal. Use Redis integration to verify actual script behavior/key-type errors/fsync ownership; unit mocks cannot establish persistence correctness. Use mock node state machines for HTTP/SSE races and real nodes for tokenizer/cancellation/native behavior. Use a fake wallet for exhaustive faults and stagenet for real external semantics. No mainnet transfers in tests.

Name tests for user-visible invariants; dependency adapters return typed, classified errors. Keep money arithmetic out of HTTP DTO mapping and avoid a generic event system for financial commands. Add a Go race test to request/WS state changes and use Rust ownership/cancellation tokens rather than mutable globals. All fixed timers are injectable clocks in tests. Migration code checks schema/version before writes and preserves financial tombstones/journal references.

## First-run verification and recovery

B0 proves actual external APIs before domain implementation. B2 gives the first full-stack slice. B3/B5 verify token IDs/slot cleanup on the pinned real runner; B7/B9 fault every monetary transition; B11 verifies supported platforms/load. Run a clean-clone check before each shared gate: contract generation, declared secret/config paths, correct stagenet daemon, model hashes, service readiness and isolated test project. Inspect changed money/state/parser code cold before running.

Provide one-command failing scenarios and content-free evidence. After two failed fixes, inspect actual pinned source/trace or wallet reply and revise the assumption; do not alter retry/accounting behavior to hide the symptom. Before production recovery, keep readiness false, fence old work, replay ledger/reconcile wallet and then open circuits through audited operator actions.

## Next actions

- **BN1 (B0):** Publish schemas/generated types and protocol fixtures. **Done when:** `make contracts-check` passes across Go/Rust/TypeScript bindings.
- **BN2 (B0):** Run tokenizer, precision/fsync and wallet prepare/relay spikes. **Done when:** `make verify-foundations` records real versioned samples and passes faults.
- **BN3 (B1/B2):** Build auth, gateway and mock-node SSE slice. **Done when:** `make smoke` returns the shared completion/usage flow through nginx and Kafka.
- **BN4 (B4/B7):** Begin independent Rust transport and Redis ledger queues. **Done when:** narrow transport/replay and balance/conservation tests pass before real money integration.

## Changelog

- v1, 2026-10-08: Define Go/Rust ownership, subsystem boundaries and B0–B11 proof gates.

## Open risks

- llama.cpp interface changes, native model parsing and GPU drivers can invalidate admission or privacy cleanup; pin/re-verify and reject incompatible models.
- Whole-text retokenization per chunk can consume server CPU; measure/batch small chunks without trusting node counts or delaying SSE beyond the budget.
- Redis Lua errors do not offer transaction rollback after prior writes; thorough prevalidation and fault tests are critical.
- An fsync timeout is ambiguous, and blockchain RPC cannot honor Redis fencing; circuits and a supervised single wallet writer are necessary.
- Canary/sampled quality checks are imperfect and may unfairly flag slow/proxied nodes; hold/release review needs transparent policy.
- User disconnects and node failures have different billing outcomes; race tests and accurate documentation must remain aligned.
- Exact OS/GPU memory zeroization is not guaranteed; logical cache clearing and no-disk/no-log tests do not constrain malicious owners.
