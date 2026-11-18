# Cordova — infrastructure and release agent plan

**Goal:** Make Cordova reproducibly deployable and recoverable with private data services, isolated Monero custody and verified portable node releases.
**Date:** 2026-10-09 (Asia/Baku). **Version:** v2. **Owner:** infrastructure agent. **Status:** Stage 1 has a Go-only GitHub Actions workflow; Stage 2 has contract schemas and a checker, but CI does not run that checker yet. Compose and deployment manifests do not exist.

Apply planiuer Core (deployment dependencies), Security (network/secrets/custody), Quality (repeatable configuration), and First-Run (clean-stack/restore/release proof). Skip none. Read [PLAN.md](PLAN.md) first; it exclusively owns shared contracts, financial policy, transport/auth rules, persistence requirements, threats, defaults and milestone gates. This file owns deployment manifests, CI/CD, operations, backups and release plumbing. Do not reproduce endpoint/message/ledger schemas here.

## Summary

First wire the Stage 2 contract checker into the existing CI workflow without requiring Docker or external services.
Then expose only nginx and keep Redis, Kafka, wallet RPC, tokenizer and workers private.
Run a default isolated stagenet Compose stack and a capped production overlay.
Require off-host Redis replica fsync for production money and disable automatic financial failover.
Use encrypted off-host backups, content-free monitoring and tested reconciliation-led recovery.
Build and sign immutable app images and Windows/Linux/macOS node bundles.
Block real-money deployment if restore, network isolation, privacy or payout-fault gates fail.

## Core 1 — Environments, topology and service boundaries

| Environment | Purpose | Defaults / boundary |
| --- | --- | --- |
| Local | Full-stack development | Local CA, localhost nginx, test credits and stagenet; one Redis/Kafka; no mainnet secrets |
| CI | Reproducible integration/adversarial tests | Isolated Compose project/volumes, mock wallet/node; dedicated stagenet job only where funded test fixture is available |
| Staging | Real model/node and wallet verification | Public staging TLS, stagenet, separate accounts/secrets/storage from production |
| Production pilot | Gated capped mainnet service | Shared A01 scale, fsynced off-host Redis replica, protected deployment, bounded hot wallet and offline cold custody |

Production uses an application host plus a separate private replica host in one region. The app host may run three Kafka brokers/controllers with independent volumes; this survives a broker-process failure, not host failure. Redis replica runs on the second host using a dedicated replica Compose file; primary promotion is an audited recovery action. Do not introduce Sentinel/automatic promotion, multiple live gateways or overlapping wallet writers in v1. This matches the restore-first availability decision, not multi-host zero-downtime HA.

### Compose services and ports

Implement these services in `infra/compose.yaml` with dev/test/prod overlays. All images are pinned by digest through a generated release manifest. Required base services are nginx, server, Redis, Kafka, Monero wallet RPC and frontend; additional workers enforce least privilege.

| Service | Internal listener / peers | Host publication | Persistent/state needs |
| --- | --- | --- | --- |
| `nginx` | 443 TLS, 80 ACME/redirect; server and frontend upstreams | Dev `127.0.0.1:8443`/`8080`; prod 443/80 only | Read-only TLS/config; no body/query logging |
| `frontend` | 8080 static SvelteKit files, edge network only | None | Immutable image; no account/private data volume |
| `server` | 8080 Go gateway; internal metrics 9090 | None | Redis/Kafka access, session/auth signing/encryption secrets; tmpfs only for runtime |
| `tokenizer` | 8090 C9 private helper; child HTTP loopback only | None | Read-only approved model volume; tmpfs runtime; no content cache |
| `event-publisher` | No public HTTP; private metrics | None | Redis outbox and Kafka producer ACL only |
| `ranker` | No public HTTP; private metrics | None | Kafka node topic consumer and Redis projection ACL only |
| `projections` | No public HTTP; private metrics | None | Authorized usage/rank topics and owner-only Redis projections |
| `payments` | No public HTTP; private metrics | None | Financial Redis ACL, wallet RPC credentials, payout writer lock, treasury attestation access |
| `redis` | 6379 native TLS in prod; local native TCP dev | None; replica-host private firewall exception only | Complete data/AOF/RDB volume |
| `redis-replica` | Native Redis TLS on separate production host | Private VPC/VPN address only, never internet | Independent AOF/RDB volume; fsync on replication |
| `kafka` / prod `kafka-1..3` | 9092 client native protocol, 9093 KRaft controller | None | Broker/controller data volumes and cluster ID |
| `monero-wallet-rpc` | 18082 authenticated private RPC | None | Encrypted hot wallet/cache; payment worker only |
| `monerod` | 18081 restricted trusted daemon RPC, chain peer egress | None by default | Blockchain volume; wallet-only RPC network |
| `backup` | No listener; controlled store access/egress | None | Encrypted remote backup credentials; approved snapshot staging |
| `prometheus`, exporters, `alertmanager` | Metrics/private operator network | None; operator VPN access only | Bounded metrics retention; alert routes contain no content |

Use a dedicated trusted `monerod` by default rather than assuming wallet RPC includes a daemon. It needs explicit public peer-network egress; it does not need a public wallet or daemon RPC port. For an external trusted daemon, require authenticated encrypted transport and documented privacy/trust trade-off, never an arbitrary public node configured silently. Initial chain sync is a readiness gate, not a healthy-wallet assertion.

### Networks and policy

| Network | Members | Policy |
| --- | --- | --- |
| `edge` | nginx, frontend, server | Only nginx host-published; server has no frontend-build access |
| `tokenization` | server, tokenizer | Internal; no nginx/wallet/Kafka access from helper |
| `data` | server, redis, event-publisher, ranker, projections, payments, approved exporters/backup | Internal native Redis only; per-role ACL and key restrictions |
| `events` | server/event-publisher, Kafka, ranker, projections/exporter | Internal native Kafka only; per-topic producer/consumer permissions |
| `wallet-rpc` | payments, monero-wallet-rpc | Internal; exclude server/nginx/frontend/backup from live spending RPC |
| `chain-rpc` | wallet RPC, monerod | Internal; daemon's RPC is restricted to wallet |
| `egress` | monerod, approved backup job and catalog acquisition job | Explicit host firewall egress rules; not a universal bridge |
| `ops` | Prometheus/exporters/alertmanager and approved service metrics | Private operator access; no public metrics/admin paths |

Docker network membership is defense in depth, not a complete firewall: enforce host/cloud deny-by-default rules including Docker forwarding chains. Limit Monero peer egress, backup destination, DNS and approved artifact sources according to the role. The server sends node commands only over existing WSS sessions; it never opens inbound ports to a community node. Across hosts use private VPC/WireGuard connectivity plus Redis TLS; never tunnel Redis/Kafka through WS. Docker-internal DNS is allowed; strict node SOCKS5 DNS behavior is independently tested by backend.

## Core 2 — Infrastructure layout and configuration

```text
infra/
  compose.yaml, compose.dev.yaml, compose.test.yaml, compose.prod.yaml
  compose.replica.yaml          # Separately deployed off-host production Redis replica
  docker/{server,frontend,tokenizer,wallet}/Dockerfile
  nginx/{nginx.conf,locations.conf,tls.conf,headers.conf}
  redis/{redis.conf,replica.conf,acl.template}
  kafka/{broker.properties,topics.json,acl-plan.json}
  monero/{wallet-rpc.conf.template,daemon.conf.template}
  monitoring/{prometheus.yml,alerts.yml,dashboards/}
  backup/{snapshot.sh,restore.sh,retention.conf,systemd/}
  release/{manifest.schema.json,node-matrix.json,verify-artifact.sh,verify-artifact.ps1}
  environments/{dev.env.example,staging.env.example,prod.env.example}
scripts/
  bootstrap-dev.sh, config-check.sh, smoke.sh, deploy.sh, rollback.sh
  restore-drill.sh, release-gate.sh, network-check.sh
.github/workflows/
  ci.yml, integration.yml, audit.yml, fuzz.yml, images.yml, deploy.yml, node-release.yml
docs/
  infra-runbook.md, backup-restore.md, wallet-operations.md, release-node.md
```

Define `make` targets in the root for shared milestone gates. Delegate language checks to scripts/package commands provided by backend/frontend; do not duplicate domain tests in shell. Keep secrets absent from `.env.example`, Compose source, generated previews and image layers. Templates name required `_FILE` inputs instead of plausible real values.

Central config inventory: `PUBLIC_ORIGIN`, `MONERO_NETWORK` default stagenet, model/catalog version and signed release manifest, role-specific Redis URL/ACL password file, Kafka bootstrap/SASL/TLS files, API pepper file, node-JWT signing key file, encryption master-key file, wallet RPC credential file, wallet path/password file, backup repository/key file, trusted cold address/attestation input and circuit defaults. Policy amounts/timeouts come from PLAN/validated application config. Missing production values fail `make config-check`; no placeholder domain or seed can pass production validation. Validate addresses/network and forbid mainnet with test-credit mode.

## Core 3 — Dependency-ordered execution tasks

All commands below are planned commands to implement. Each task has an executable gate; dependencies refer to [shared milestones](PLAN.md#core-3--dependency-ordered-milestones).

### I0 — Gate the Stage 2 contract foundation (Stage 2 / B0; first; S)

Extend the existing `.github/workflows/ci.yml` with a repository-root contract job. Run `sh scripts/contracts-check.sh` on pushes and pull requests using the existing read-only workflow permissions and pinned action revisions. Keep the current Go format, vet, race-test and build checks. The checker uses Python's standard library; do not add a contract-tool dependency or require Docker for this gate. The job must validate the Stage 2 OpenAPI, node/dashboard WS and tokenizer schemas and their accepted/rejected fixtures as documented in [Stage 2](docs/stage-2.md).

Keep contract validation separate from B0 external integration evidence. A green CI job does not prove Redis Lua/`WAITAOF`, Kafka outbox/replay, tokenizer/model parity, SOCKS5h, wallet behavior, Compose isolation or nginx behavior. Record each real probe and any unavailable prerequisite in `docs/verification.md`; unavailable is not passed. Backend owns schema semantics and fixtures; infra owns invoking their check consistently in CI.

**Done when:** a clean push/PR workflow runs `sh scripts/contracts-check.sh` alongside all Stage 1 Go checks; schema or fixture violations fail the contract job; the job needs no secrets, Docker daemon or external services; the recorded CI result is distinguishable from all still-open B0 integration probes.

### I0b — Verify versions, inputs and reproducibility (M0; after I0; S)

Inventory actual Docker/Compose, host OS/CPU/memory/disk, Go/Rust/Node/CMake and GPU release-runner tools before choosing image flags. Pin maintained compatible patches/digests and lock CI actions to commit SHAs. Confirm `WAITAOF`, KRaft bootstrap/SASL, nginx streaming/upgrades, wallet digest auth/daemon sync and replica TLS against pinned images; capture content-free real samples with backend B0. Verify model/dependency redistribution licenses and source checksum/signature procedures. Name all config/secret inputs in one validation schema with safe dev defaults and mandatory production errors.

**Done when:** `make config-check && make verify-infra-foundations` runs from a clean clone, lists exact versions/digests and demonstrates private native Redis/Kafka, local TLS, wallet isolation and production-required secret validation.

### I1 — Build Compose, edge and mock-node slice (M1; I0b; parallel with backend/frontend; M)

Build multi-stage non-root images; keep compilers/package caches out of runtime. Add health checks for process/dependency readiness, resource limits, restart policies, read-only root filesystems, tmpfs runtime paths, `cap_drop: ALL`, `no-new-privileges`, unprivileged users and disabled core dumps where supported. Grant only required volumes/writes; no Docker socket mount. Use nginx static frontend deep-link fallback and private server upstream routes. `depends_on` helps initial order but every service must handle reconnects/failures after startup.

Implement `scripts/bootstrap-dev.sh` to generate ignored local secret files and a local CA using secure permissions, select stagenet and start without manual edits. Trust that CA only in the isolated test/browser setup; never use TLS verification bypasses in production. Add Compose test profile for backend mock node/wallet with no real wallet material. Ensure `down` retains volumes; prohibit deploy scripts from using `down -v` against non-test projects.

**Done when:** `docker compose -f infra/compose.yaml -f infra/compose.dev.yaml up --build --wait` plus `make smoke && make network-check` passes in a fresh clone; models/SSE/dashboard updates traverse nginx and only intended nginx localhost ports are reachable.

### I2 — Configure nginx TLS, WS and SSE (M1/M2; I1; S)

Implement the configuration policy below; verify all directives against the pinned nginx build. Acquire/renew production certificates through a secret-mounted ACME mechanism; monitor expiration and reload only after `nginx -t`. Redirect HTTP except ACME challenges, enforce TLS 1.2/1.3, HSTS and modern ciphers. Add CSP/frame/referrer/nosniff headers for UI without breaking compiled assets. Use safe route-template access logs or disable access logs for sensitive routes; never log raw URL/query/header/body, IP, prompt, output or wallet address. Restrict `/readyz` and `/metrics` to private ops access.

**Done when:** `make test-edge` proves valid certificate/hostname, rejected old TLS, HSTS on HTTPS, WS upgrades and heartbeats, immediate SSE delivery, deep-link refresh, safe logging and 404/deny for public metrics/readiness/wallet paths.

### I3 — Protect and restore state (M3/M5; I1 + backend ledger scripts; parallel with payment development; L)

Implement Redis persistence/ACL/resource policy below, including production replica and application fsync checks. Measure RDB/AOF fork/rewrite peak memory; size host/disk for at least 2× observed Redis peak RSS plus other services, leave ≥30% free disk and pause new requests before storage exhaustion. Configure primary writes to require one healthy replica (`min-replicas-to-write 1`, max lag 5s), while application C5 still checks actual fsync acknowledgements. No eviction; no automatic replica promotion. Redis role/replication changes require an audited operator action and recovery gate.

Schedule encrypted off-host backups and immutable financial archives using the backup policy below. Implement a scratch restore drill with no production endpoints/credentials, ledger replay, orphan-reservation refund, wallet snapshot association and reconciliation simulations. A funded system remains closed after restore until old writers are stopped and newer on-chain activity is reconciled. Keep restored account keys disabled until the operator has verified whether revocation happened after the restored snapshot.

**Done when:** `make restore-drill && make test-redis-durability` kills primary/replica around fsync barriers, restores from off-host media, detects ambiguous/newer wallet events and produces matched ledger balances without reopening billing prematurely; measured RPO/RTO meet A18.

### I4 — Operate Kafka and wallet isolation (M4; I1/I3; parallel with rank/payment adapters; M)

Provision C7 topics/keys/retention and role ACLs; auto-topic creation is disabled. Dev uses one combined broker/controller; production uses three KRaft brokers/controllers, replication factor 3, minimum in-sync replicas 2 and producer acks=all. Do not imply three brokers on one host protect against host loss. Persist cluster ID/metadata volumes; broker restarts reuse formatted state instead of formatting anew. Use SASL/SCRAM for client identity, and TLS for cross-host links; controller endpoints stay private. Test one-broker failure and replay-safe rank/usage projections. Preserve financial outbox through Kafka outage; no ledger mutation depends on successful Kafka consumption.

Isolate the Monero services and payment worker as the topology table requires. Enable wallet RPC authentication, keep spending method access private, mount only the hot wallet, validate the selected network and block readiness until daemon/wallet are synchronized. Serialize deployment/restarts of the payment writer: stop it, wait for pending RPC and reconcile its last batch before starting another instance. Seed/key backups use a dedicated encrypted workflow, not logs. Provide operator cold-wallet sweep/top-up and signed attestation steps, with amount/network/txid verification and ledger recording. Never put cold spend keys in runtime containers.

**Done when:** `make test-events-ops && make test-wallet-isolation` proves unauthorized Kafka/Redis peers denied, single-broker failure recovered, wallet RPC unreachable from nginx/server/frontend/host public interfaces and no overlapping payout writer during rolling/deployment faults.

### I5 — Build CI/CD and immutable image deployment (M1–M5; I0/I1; final gates after M4; M)

Implement the pipeline below with strict job dependency order and isolated fork-PR permissions. Build once and promote the same signed image digests to staging/production. Stage deployments on stagenet and run shared contract/system/browser/security gates before production qualification. Implement preflight snapshot/version/schema checks, process drain and rollback with the financial safety rules below. Keep deploy credentials in protected environments with short-lived identity where available; build/test jobs get none.

**Done when:** a clean PR pipeline passes or blocks each required gate, a staging release deploys pinned digests and passes `make release-gate`, and `make test-deploy-rollback` proves compatible rollback and payment-writer exclusivity under injected health/migration failures.

### I6 — Release and verify portable node binaries (M2/M5; backend B4/B5 + I0; parallel with dashboard; L)

Build the platform matrix below from signed version tags and locked sources, including the same approved llama.cpp commit. Bundle the Rust executable, platform llama.cpp executable/runtime libraries, notices, schema version, source provenance, model-manifest verification key and artifact verification instructions. Do not bundle wallet/API secrets or model prompts. Produce signed checksums and SBOM/provenance; release only artifacts whose platform smoke passes. Upload verified bundles as draft artifacts first; publishing is an explicit release workflow action by the repository's protected tag process.

Implement signed manifest/checksum verification for download/install; the node refuses tampered runner binaries/catalogs. Provide foreground install/run and optional user-level background startup for systemd user (Linux), launchd user (macOS) and Task Scheduler user (Windows), matching backend zero-config login. Avoid automatic privileged installation. Native code signing/notarization is required for advertised official macOS/Windows releases; if credentials are absent, emit development artifacts clearly labeled unsigned and do not promote them as official.

**Done when:** `make test-node-releases` downloads/verifies each candidate bundle in clean Windows/Linux/macOS environments, runs login/run/status/reconnect/shutdown and proxy smoke, detects tampering and leaves no child process/content files; all official artifacts include signed checksums and applicable native signing evidence.

### I7 — Add monitoring and exercise incident recovery (M5/M6; I3–I6; M)

Install content-free metric collection and alert rules below. Restrict operator dashboards to VPN/SSO or SSH-tunneled access; do not expose a new public admin surface. Map each alert to the runbook action, owner and relevant circuit control. Run a one-hour load/connection-churn test with backend B11, backup restore, cert-renewal failure, unavailable replica/Kafka/wallet, model warmup and unknown payout drills. Verify each incident preserves ledger/audit and shows truthful service readiness.

**Done when:** `make monitoring-smoke && make incident-drill && make release-gate` sends test alerts to the configured operator destination, proves circuits/readiness and recovery steps and produces content-safe evidence with no secret/prompt labels; production mainnet remains disabled unless the shared M6 gates pass.

## Edge configuration policy

Use an `http`-level upgrade map, a HTTPS virtual host and separate ordinary REST, WS and SSE locations. Derive actual upstream paths from C2 rather than maintaining a second endpoint registry. Example directives illustrate the required behavior; render cert paths/origin from validated configuration and run `nginx -t`.

```nginx
# http context
map $http_upgrade $cordova_connection_upgrade {
    default upgrade;
    ''      close;
}

# HTTPS server context
ssl_protocols TLSv1.2 TLSv1.3;
add_header Strict-Transport-Security "max-age=31536000" always;
client_max_body_size 1m;

# WS locations selected from C2
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection $cordova_connection_upgrade;
proxy_read_timeout 75s;
proxy_send_timeout 75s;

# /v1/chat/completions location (also handles non-streaming)
proxy_http_version 1.1;
proxy_buffering off;
proxy_request_buffering off;
proxy_cache off;
gzip off;
proxy_read_timeout 150s;
proxy_send_timeout 150s;
send_timeout 30s;
proxy_max_temp_file_size 0;
client_body_buffer_size 1m;
```

Forward normalized Host/proto/client connection information only from the trusted edge; Go rejects untrusted forwarded headers. Set a bounded HTTP header timeout and ordinary REST timeout without imposing it on WS/SSE. Disable request-body temporary files: backend's 1 MiB limit and nginx's full body buffer must be tested with a 1 MiB synthetic prompt, including nonstreamed REST requests. Keep request body buffering disabled on content paths and verify nginx does not write a body to disk when upstream slows. No cache applies to API/SSE/private app responses. Static immutable assets can be cached; static app shell never embeds private account content.

Upgrade/Connection headers need explicit proxy forwarding, and SSE requires disabled response buffering. Verify both with the official pinned configuration interface, then test first-byte behavior. [nginx WebSocket proxying](https://nginx.org/en/docs/http/websocket.html), [nginx proxy buffering](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering).

## Persistence, volumes and backup policy

| Data | Volume/policy | Scheduled off-host protection |
| --- | --- | --- |
| Redis | Encrypted `redis-data`; full multi-part AOF directory and RDB, noeviction, `appendonly yes`, `appendfsync everysec`, RDB `save 900 1`, `save 300 100`, `save 60 10000`; stop writes on snapshot errors | Consistent RDB snapshot every 15min; daily coordinated filesystem snapshot of complete AOF+manifest/RDB; restore tested monthly |
| Financial ledger archive | Canonical balanced records and signed hash-chain checkpoint; encrypted immutable object storage | Export contiguous ledger batches every minute; verify checkpoint continuity daily; retain five years per A15 |
| Wallet | Encrypted `wallet-data`, secrets separate; full wallet/cache not just seed | Serialize wallet `store` and coordinated encrypted snapshot every 15min and before release/treasury action; offline seed backup verified separately |
| Kafka | One persistent broker/controller volume each, retention from C7 | Nightly encrypted metadata/config backup; node/usage projections recover from Redis/outbox/recent event replay; ledger is independent |
| Catalog/tokenizer models | Read-only verified model cache and signed manifests | Versioned approved artifacts/source checksums in remote release storage; reproducible conversion recipe |
| TLS/config | Secret store and committed templates | Secret-store recovery policy; encrypt any necessary offline recovery bundle; no secrets in source archive |
| Metrics/logs | Content-free bounded volumes | Metrics 14d and logs 7d; no prompt data; no mandatory long-term log backup |

Use restic or equivalently verified encrypted backup tooling, a different provider/account than the app host, versioned access and object-lock retention for financial archives. Default retention for snapshots: 48 hours of 15min snapshots, 30 daily, 12 monthly; five-year journal archives are separate. Snapshot creation must be coherent: use streaming Redis RDB export or a completed RDB/fs snapshot, never an arbitrary live copy of a changing multi-part AOF directory. Keep the associated ledger head/release/schema/wallet height in an encrypted manifest. Restore from one manifest and reconcile later activity explicitly; do not combine unrelated snapshots and assume consistency. [Redis persistence behavior](https://redis.io/docs/latest/management/persistence/).

Back up after creating consistent artifacts, encrypt before off-host transfer and verify remote integrity. Alert when the latest completed remote snapshot is >20min old; pause payouts at >60min backup lag until resolved. Retain source snapshots until successful remote verification. Never let backups fill Redis/wallet working disks; use separate staging quota. A nightly integrity read and monthly full scratch restore proves keys/access/contents. Record observed RPO/RTO; an untested backup is not a passing M5 gate.

Restore order: close edge/inference/billing/payout circuits → stop old gateway/wallet writer and fence access → restore secrets/config and coherent Redis/wallet snapshot on private scratch/target host → verify journal/hash chain and schema → rebuild Kafka projections → perform idempotent orphan refunds → rescan wallet/chain and resolve newer or unknown payouts/credits → compare backing/liquidity → rotate/disable credentials affected by missing revocations → run smoke and explicitly reopen. Do not relay a restored `prepared` batch until reconciliation proves its current status.

## CI/CD stage order and acceptance

| Stage/task | Required commands or checks | Depends on / parallelism | Done when |
| --- | --- | --- | --- |
| C0 — Validate contracts/config/secrets | Stage 2: `sh scripts/contracts-check.sh`; later: YAML/Compose/schema checks, secret scan including Git diff and generated artifacts | Stage 2 contract gate first; deployment checks join when their files exist | **Done when:** Contract drift and secret fixtures are rejected in Stage 2 CI; later infrastructure checks also reject malformed config or public wallet/Redis/Kafka ports |
| C1 — Lint/typecheck/static analysis | Go fmt/vet/gosec; Rust fmt/clippy with denied warnings; frontend ESLint/Svelte/TypeScript checks | C0; language jobs parallel | **Done when:** All language jobs pass without unreviewed suppressions |
| C2 — Test | Go unit/race; Rust workspace; frontend unit; Redis/Kafka/mock-node system tests and Playwright; privacy/parser corpus tests | C1; unit jobs parallel; integration needs built test artifacts | **Done when:** Shared success/error/accounting/access fixtures pass with isolated test volumes |
| C3 — Audit dependencies/native inputs | `govulncheck ./...`, `cargo audit`, `npm audit --audit-level=high`; license/native llama.cpp review; scheduled parser fuzzing | C0; parallel with C1/C2; all required before build promotion | **Done when:** No unreviewed relevant vulnerability/license violation; valid exceptions have owner/expiry |
| C4 — Build images/binaries | Locked multi-stage OCI builds, SBOM, pinned llama.cpp/native checksums | C1–C3 | **Done when:** Rebuilt artifacts have expected content/provenance and contain no secrets/dev data |
| C5 — Scan/sign | Trivy image scan, secret scan of image layers/assets, cosign signature/provenance | C4 | **Done when:** No high/critical actionable findings or forbidden files; signatures verify |
| C6 — Deploy staging and qualify | Immutable digest deployment, migration/drain/restore/rollback tests, real-model/stagenet gates | C5 | **Done when:** Staging runs same digests and `make release-gate` passes with content-safe evidence |
| C7 — Deploy production pilot | Protected environment, M6/legal gate, preflight snapshot, schema checks and operator-controlled mainnet config | C6; serialized deployment | **Done when:** Production smoke/reconciliation/replica fsync pass, prior deployment can be restored safely and no overlapping wallet writer exists |

Pin action SHAs, use read-only default token permissions, avoid `pull_request_target` execution of untrusted code with secrets, and isolate artifact upload/deploy jobs from fork PRs. Scheduled daily jobs rerun audits/fuzzing and alert only on a new failure/meaningful change. Pull dependencies through verified registries and cache only non-secret build artifacts. Native model/runtime artifacts need provenance checks because `cargo audit` does not audit llama.cpp or model files. CI prints commands/environment/test counts and labels unavailable external GPU/stagenet checks as unavailable, never passed.

Production rollback is code-only within a backward-compatible schema range. Drain gateway and stop payment writer before migrations. Use versioned additive migrations, backup before financial/schema change, validate prior-version reads and apply dual-read/single-write when needed. If a release created incompatible state or broadcast payments, do not roll back Redis/wallet volumes to erase it; close circuits and perform a forward repair/reconciliation. Keep last two signed release manifests/images, exact schema compatibility ranges and node protocol support. Do not force-update a running node in the middle of a request.

## Node release matrix

| Target | Baseline bundle | Required release evidence |
| --- | --- | --- |
| Linux x86_64 | Rust node + CPU llama.cpp; glibc baseline fixed/tested in M0 | Clean supported distro smoke; systemd user optional install; signed archive |
| Linux aarch64 | Rust node + CPU llama.cpp | Native ARM64 smoke, not only cross-compilation; signed archive |
| Windows x86_64 | `.exe` node/runner + required runtime DLLs | Supported Windows 11 smoke, Task Scheduler user setup, Authenticode for official release |
| macOS x86_64 | Node/runner, CPU fallback and tested Metal where supported | Intel macOS smoke; codesign/notarization for official release |
| macOS aarch64 | Node/runner with Metal and CPU fallback | Apple Silicon native smoke; launchd user setup; codesign/notarization |

Optional CUDA/Vulkan flavors require a real matching GPU/driver smoke job; label experimental if not tested. Do not claim a CPU fallback artifact supports all GPU backends. Build Rust and llama.cpp with compatible deployment targets and record minimum OS/driver/CPU features in manifest. Sign checksums with an offline/protected release key, attach source commit/SBOM/build provenance and verify server/model manifest trust keys in the installer. Native signature credentials are secret-store inputs; protected releases fail if official signing is required and missing.

Provide Bash/PowerShell verification examples, manual archive install and package/install helpers after their tests pass. Cache directory contains signed model weights only; credentials use OS keychain/fallback policy from C3. Auto-update checks fetch signed metadata, download/verify in background and apply after drain; disable unattended updates by default in v1 and publish explicit update steps. Test interrupted extraction, rollback and tampered checksum/runner detection.

## Monitoring and incident controls

| Signal | Default alert threshold | Runbook response |
| --- | --- | --- |
| Financial mismatch / uncertain write | Any nonzero unexplained difference or fsync failure | Close billing/payout circuit; inspect operation ID and reconcile, never overwrite balance |
| Orphan reservation / lease | Older than request deadline +60s | Reaper/recovery check; refund only through idempotent operation |
| Redis AOF/RDB/replica | AOF error or unhealthy required replica immediately; headroom <30% disk or >75% configured memory for 5min | Pause new work; preserve state; restore replica/storage before reopening |
| Kafka/outbox | Rank lag >60s for 5min; pending outbox >512MiB | Repair broker/consumer; pause new requests before 1GiB pending limit; preserve financial events |
| Wallet/chain/deposits | Sync lag >5 blocks for 5min or wallet unavailable | Pause credits/payouts, show safe billing status, verify daemon |
| Unknown payout / hold | Any unknown payout; hold awaiting review >24h | No recreation/retry; query stored hashes, reconcile and audit operator action |
| Backup/cert | Remote snapshot >20min old; certificate expiry <14d | Repair backup/renewal; >60min backup lag pauses payouts |
| Node capacity and request failure | 503 capacity >20% for 10min; node-failure rate >5% for 5min | Inspect warmup/lease/benchmark health; cap intake and investigate affected model |
| Tokenizer / streaming | p95 overhead exceeds B11 budget for 10min or unbounded queue | Reduce intake; profile helper/chunk batch sizes; never use untrusted token counts |
| Fraud/privacy | Protocol-fraud event or failed privacy sentinel immediately | Quarantine/fence node or block release; audit holds; preserve approved metadata only |

Operator controls are authenticated CLI/runbook actions backed by durable circuit keys, not public dashboards. Alerts carry safe event IDs/codes and operational links, never prompt text/payment address/secrets. Default operator destination is configured at deployment; test deliveries use synthetic content. Incident drills prove secret rotation, stop/drain, no duplicate relay, off-host recovery and explicit circuit reopen.

## Core 4 — Assumptions

- Follow A01–A19/D01–D11 unchanged. Dev/staging use stagenet; mainnet requires shared M6 qualification.
- Use a separate private Redis replica host for production; brokers may share the app host with explicit host-failure downtime risk.
- Use a secret manager where available and mounted 0400/0600 secret files for local/test environments; env carries paths rather than raw seeds/passwords when supported.
- Operators provide actual domain, backup repository, alert destination and custody/signing keys through declared config. Fail safely when absent; no agent invents a usable public resource or publishes a release while writing plans.
- Use adult capped pilot accounts, a 1 XMR initial hot-wallet funding ceiling and the shared fee/hold policy. Financial changes are ledger-aware deployments, not generic blue/green switches.
- Only approved model/runtime/config/credential files may be writable on nodes; managed servers disable swap/core dumps. User hosts need documented OS privacy limitations.
- All new commands/configuration paths in this file are implementation deliverables. Existing `.old` deployment scripts are unrelated and must not be reused as evidence.

## Core 5 — Decisions, security and quality

Use [PLAN Decision log](PLAN.md#decision-log), threats T08–T13 and the shared data inventory. Do not duplicate financial defaults into CI YAML without deriving them from validated config. Keep role ACL/network policy auditable and test denial cases; password-authenticated services on an “internal” bridge alone are insufficient. Encrypt host disks and backups; separate key restoration from data restoration, and test both.

Lint shell scripts (`shellcheck`), YAML/actions (`actionlint`), Dockerfiles (`hadolint`) and Compose renderings. Use task-specific variables, quoted arguments and structured secret references. Keep scripts idempotent, fail-fast and clear about environment/project; destructive cleanup is limited to uniquely named disposable test projects. Code/financial schema migrations must retain append-only tombstones and audit history; rollout cannot silently reset a wallet or reformat Kafka storage.

## First-run checks and failure handling

I0 verifies actual flags/tool APIs; I1 proves a clean clone; I2 verifies edge buffering/disk behavior; I3 proves remote restoration; I4 verifies Kafka/wallet access; I5/I6 prove deployment/native release behavior; I7 exercises incidents. Before each run confirm intended environment, network, secret references, disk/daemon sync, schema/image compatibility and health checks. Print a safe preflight summary; never echo secrets or whole rendered Compose with secret values.

On a failed deployment, preserve service/ledger/wallet state, close affected circuits and reproduce with one command. After two unsuccessful fixes, inspect the actual pinned image/source and update verification evidence. Reopening requires the relevant shared gate to pass; time elapsed or an apparently healthy container is not proof of financial correctness.

## Next actions

- **IN0 (Stage 2 / I0):** Run the existing contract checker in the pull-request workflow. **Done when:** contract/schema/fixture violations fail CI and the workflow remains independent of Docker and external services.
- **IN1 (I0b):** Pin versions/digests and implement configuration validation. **Done when:** clean-clone `make config-check` rejects absent production secrets and invalid network/model combinations.
- **IN2 (I1/I2):** Bring up private Compose and nginx with mock-node SSE. **Done when:** `make smoke && make network-check && make test-edge` passes.
- **IN3 (I3):** Configure persistence/replica and consistent encrypted remote backups. **Done when:** a scratch restore passes `make restore-drill` and records measured RPO/RTO.
- **IN4 (I5/I6):** Add blocking language/security pipeline and portable artifact builds. **Done when:** a deliberately bad audit/protocol fixture blocks promotion and baseline node bundles pass platform smoke.

## Changelog

- v1, 2026-10-08: Define infrastructure topology, deployment/release ownership and I0–I7 gates.
- v2, 2026-10-09: Add the Stage 2 contract-CI gate; distinguish it from unverified M0 integrations and the later Compose/deployment slices.

## Open risks

- Single application host failure stops gateway/Kafka/wallet service; a replica and backups protect state without claiming uninterrupted availability.
- Docker forwarding behavior can undermine host firewall assumptions; external/private peer scans must prove actual exposure.
- Redis backup consistency, replica fsync and wallet cache freshness can diverge during failures; restore manifests and reconciliation are mandatory.
- Native signing/GPU runners may be unavailable; unsigned/untested variants must remain development artifacts.
- nginx buffering or temporary-body files can violate privacy even if application logs are clean; large/slow-stream sentinel tests are required.
- Backup/signing/secret-store recovery depends on real operator access; an unrehearsed key recovery process can make encrypted backups unusable.
- Custodial incident handling and mainnet legal eligibility need responsible operators and qualified review before real funds are accepted.
- Stage 2 CI can prove contract/fixture consistency only; Redis, Kafka, tokenizer/model, SOCKS5h, wallet and container-network behavior remain open until their pinned integration probes run.
