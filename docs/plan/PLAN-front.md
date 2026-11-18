# Cordova — frontend agent plan

**Goal:** Implement a clear account dashboard and public setup documentation for Cordova using TypeScript and Svelte.
**Date:** 2026-10-08 (Asia/Baku). **Version:** v1. **Owner:** frontend agent. **Status:** F0 contract generation, F1 account/navigation and F2 typed REST/live frontend are implemented. F3 now includes owner approval, paginated inventory, detail, rename and revoke against session-protected APIs; model commands, live node updates and full M2 integration remain open. Fixture-backed Playwright covers UI logic; a separate M1-stack Playwright gate exercises registration, real mock request usage invalidation and account isolation through nginx. M1 completion depends on that integration gate passing.

**Pilot update (2026-10-09):** `frontend/` now provides a bilingual dark static SvelteKit shell with overview, declared single-host configuration, authenticated live mock catalog and API documentation. The pilot uses the existing stage-1 Bearer API. The interface uses platform system typography, a readable hierarchy, consistent cards and clear interaction feedback while retaining Cordova's own visual identity. The mock pilot still has no real inference, billing or measured node telemetry. Health latency samples belong only to the current browser session. Scope and setup: [docs/frontend-pilot.md](docs/frontend-pilot.md).

**M1 frontend update (2026-10-10):** `/` is a product entry and the existing mock pilot is at `/pilot`. `/login`, `/register`, `/recover` and protected `/dashboard` implement F1; same-origin typed account REST, request-history snapshots and C4 live invalidation implement the frontend portion of F2. `/dashboard/nodes` links an authenticated one-time enrollment, lists the signed-in account's nodes with pagination/search/status filters, and provides owner-only details, rename and revoke. Unmeasured telemetry, rank and earnings remain null and are shown as unavailable. F3 model commands and live node updates still depend on later catalog/lease work. F0 generates frontend DTO types and schema-derived validators from shared OpenAPI/WebSocket contracts, checks generated-file drift, and exercises positive/negative REST and live fixtures. Fixture-backed Playwright covers registration/recovery-code handling, deep-link protection, recovery cooldown, logout, language switching, account isolation, authoritative REST refresh after live invalidation, and node-management flows. The M1 Compose gateway implements the account and live endpoints; a separate browser scenario exercises registration, test-key mock inference, live usage refresh and account isolation through the HTTPS nginx edge. Recovery codes and live cursors stay in memory. The mock output and usage remain synthetic and non-billable.

Apply planiuer Core (pages and dependencies), Security (credentials and account data), Quality (typed components), and First-Run (real contract/browser checks). Skip none. Read [PLAN.md](PLAN.md) before implementation: it exclusively owns shared API/WS schemas, authentication, privacy statements, pricing, money types and milestone gates. This file owns routes, components, state, frontend verification and user-facing behavior. Do not redefine a shared contract here.

## Summary

Build a responsive Svelte dashboard with nodes, API keys, docs, billing and analytics.
Use generated contract types and same-origin browser APIs; show authoritative server values.
Keep secrets only in their short-lived reveal views and use cookie sessions for the dashboard.
Start with the session/catalog shell against fixtures, then connect to M1's live mock-node slice.
Block release if cross-account access, privacy disclosure, monetary precision or keyboard flows fail.

## Core 1 — Functionality and routes

Model navigation and information density on the OpenAI platform dashboard: left navigation, a focused content region, clear tables and concise contextual help. Create Cordova's own visual identity; do not copy logos, proprietary assets or imply affiliation. A user may both consume the API and own nodes within one account; avoid separate balances invented by the frontend.

| Svelte route | Page content and behavior | Contract references |
| --- | --- | --- |
| `/` | Product entry, honest plaintext privacy warning, sign-in/register links and documentation links | PLAN Security 4 |
| `/login` | Username/password, optional TOTP, generic auth failures, recovery link and safe return path | C2 auth |
| `/register` | Username/password, privacy notice acknowledgement, one-time recovery-code reveal and explicit save acknowledgement | C2 registration; A07 |
| `/recover` | Username, recovery code and new password; disclose payout cooldown after recovery | C2 recovery |
| `/dashboard` | Balance summary, usage summary, own-node count/status, next action and operational incident banner | C1 Balance/Analytics/Node |
| `/dashboard/nodes` | Own nodes table, search/status filter, onboarding link-code form, link/download/setup actions | C2 nodes/enrollment |
| `/dashboard/nodes/[id]` | Node status/rank freshness, measured throughput, reported hardware versus tested capacity, per-model state, earnings/hold status, rename/revoke | C1 Node; C6 ranking |
| `/dashboard/keys` | Key table with name/prefix/scopes/model access/cap/last-used, create dialog, one-time secret reveal and revoke confirmation | C1 ApiKey; C2 keys |
| `/dashboard/billing` | XMR available/reserved balances, deposit address/network/QR, deposit progress, financial history and owner earnings | C1 billing DTOs; C8 |
| `/dashboard/billing/payouts` | Mature/pending/held earnings, automatic payout schedule, threshold, destination setup/change, TOTP step-up and payout statuses | C2 payout settings; A05/A06; C8 |
| `/dashboard/analytics` | UTC date/model filters, requests/tokens/spend, latency chart with table equivalent, recent request outcomes and status links | C1 Analytics/UsageRow |
| `/dashboard/settings/security` | TOTP enrollment, recovery-code instructions, session logout and account-closure/export operator instructions | C2 TOTP; PLAN Security 4 |
| `/docs` | Documentation index: API, node setup, privacy, billing and troubleshooting | Shared contract links |
| `/docs/api` | OpenAI base URL, model list, curl/TypeScript examples, supported parameters, SSE usage/errors, status lookup and idempotency limitation | C2 OpenAI subset |
| `/docs/node-setup` | Verified Windows/Linux/macOS install, `cordova-node login`, link-code approval, automatic run, proxy setup and shutdown | C3; backend release artifacts |
| `/docs/privacy` | Full threat-limit statement, second-node sampling, content retention, metadata retention and account closure | PLAN Security 1/2/4 |
| `/docs/billing` | Piconero/XMR display, rates, reservation/refund/cancellation, confirmation progress, owner share/holds, payout fee policy and reconciliation delays | C5/C8; A04–A10 |
| `/docs/troubleshooting` | Unknown versus warming model, unavailable capacity, insufficient funds, no WS connection, expired key, slow/proxied node and unknown payout | C2/C3 safe codes |

The node setup page shows platform-specific commands verified by the backend/infra release gates; do not invent release URLs or claim a binary exists before it is published. Display unavailable downloads as unavailable. Do not implement an inference playground in v1; dashboard analytics never loads/stores prompt text.

## Core 2 — Stack, file layout and component ownership

Use the frontend stack and version lock in [PLAN Core 2](PLAN.md#core-2--stack-and-architecture). Static SvelteKit output keeps the runtime backend in Go; cookie auth and route guards remain browser/API responsibilities. Use Svelte runes for local UI state and a small typed account-resource store for server snapshots. Prefer this to Redux or a second fetching framework until a measured need exists. Use native `fetch`/`WebSocket`, not a second auth SDK.

```text
frontend/
  package.json, package-lock.json, svelte.config.js, vite.config.ts, tsconfig.json
  src/app.html, src/app.css
  src/lib/api/
    generated/              # Read-only contract-generated types/client definitions
    client.ts               # Same-origin fetch, credentials, CSRF, errors and cancellation
    errors.ts               # Safe code-to-user-message mapping
  src/lib/live/
    connection.ts           # Ticket, WS state, heartbeat, backoff and cursor
    invalidation.ts         # Topic/resource revision dedupe and refetch scheduling
  src/lib/state/
    session.svelte.ts       # Session bootstrap and logout clearing
    resources.svelte.ts     # Account-scoped cache, status, revision and invalidation
    filters.svelte.ts       # Validated URL filter state
  src/lib/domain/
    money.ts                # Exact decimal-string/bigint XMR formatting and validation
    time.ts                 # UTC filters and labeled local display
  src/lib/components/
    layout/                 # AppShell, Sidebar, Topbar, MobileNav, PageHeader
    common/                 # Button, Field, Dialog, DataTable, EmptyState, StatusBadge
    nodes/                  # NodeTable, NodeDetails, ModelStates, LinkNodeForm
    keys/                   # KeyTable, CreateKeyDialog, SecretReveal, RevokeKeyDialog
    billing/                # BalanceCards, DepositAddressCard, DepositTable, LedgerTable
    payouts/                # PayoutTable, PayoutAddressForm, StepUpDialog, HoldNotice
    analytics/              # MetricCards, UsageTable, LatencyChart, AccessibleChartTable
    security/               # TotpSetup, RecoveryCodeReveal, PrivacyNotice
  src/routes/
    +layout.ts, +layout.svelte, +page.svelte
    login/, register/, recover/, docs/[...slug]/
    dashboard/+layout.svelte
    dashboard/nodes/, dashboard/nodes/[id]/, dashboard/keys/
    dashboard/billing/, dashboard/billing/payouts/, dashboard/analytics/
    dashboard/settings/security/
  static/                   # Local fonts/icons and versioned docs assets
  tests/unit/               # Money, API errors, cache/live state and form behavior
  tests/browser/            # Playwright user workflows and accessibility
```

Render `/dashboard/+layout.svelte` as `AppShell → Sidebar/MobileNav + Topbar + page slot + LiveStatus + IncidentBanner`. Page components own layout; domain components own their form/table behavior. Keep API effects in the client/store, not in presentational cells. `src/lib/api/generated/` is a read-only import bridge to `contracts/generated/typescript/`, not a second hand-maintained schema. Import documented node setup content from `docs/` or generated release metadata without hand-copying shared parameter lists.

## Core 3 — Dependency-ordered execution tasks

Each task includes a testable Done when line. Implement/check one route or component slice at a time. M numbers refer to the [shared milestone table](PLAN.md#core-3--dependency-ordered-milestones).

### F0 — Verify and consume contracts (M0; first; S)

Read C1–C4, Security 4 and A01–A19. Run Node/Svelte/TypeScript version checks against the pinned lock. Create `npm run contracts:check` that regenerates frontend types in a temporary directory and detects drift. Capture actual positive/negative fixtures for session, nodes, keys, balance, usage, live auth/event/resync and every safe error code. Validate fixtures at runtime in tests; production JSON decoding uses generated schema-derived validators for boundary payloads. If backend schema generation is not ready, implement against fixtures transcribed from C1–C4 and replace them through the same generator in M0, without altering shared shapes.

**Done when:** `npm ci && npm run contracts:check && npm run check` passes in a clean frontend directory; fixture decoding rejects numeric money, wrong enum, missing resource ID and foreign WS message types.

### F1 — Build session and navigation shell (M1; F0; parallel with backend/infra slice; M)

Implement static routing/fallback, session bootstrap, register/login/recover/logout, protected dashboard layout, recovery-code reveal and privacy acknowledgement. Validate return paths as local routes; reject protocol-relative/external redirects. Load session before private resource requests, cancel all requests and clear cache/cursors on logout/401/account change. Show a neutral loading state while auth is unresolved. Build desktop/sidebar and mobile navigation with explicit active-page labels.

**Done when:** `npm run test:unit -- session && npm run test:browser -- auth` proves register/login/logout/recover and direct deep-link refresh through nginx; a second account never sees the first account's cached content or live events.

### F2 — Implement typed API and live state (M1; F0/F1; S)

Centralize credentialed same-origin requests in `client.ts`; attach CSRF to mutations, convert contract errors into safe typed results, and support AbortSignal. Never transparently retry key creation, payout setting changes or enrollment approvals. On 429 show the server retry delay; on 503 show warming/capacity/wallet/storage-specific guidance. Use `request_id` for support/status lookup without displaying raw server exceptions.

Connect live socket using the ticket/first-frame protocol in C4, subscribe after readiness, retain cursor only in account-scoped memory and validate each frame. Dedupe invalidations by event ID/revision, debounce fetches, refetch authoritative REST data and mark stale snapshots visibly. On resync clear account-resource snapshots and reload. Reconnect using bounded jitter; switch to polling on disconnected state and stop polling/socket on logout or hidden tab after completing in-flight safe reads. Resume with a resync on visibility change if stale.

**Done when:** `npm run test:unit -- live` covers duplicate, reordered and missing events, expired cursor, malformed frame, slow consumer, reconnect, ticket failure and logout; browser mock socket tests show a changed balance only after the confirming REST fetch.

### F3 — Build node management (M2/M4A; F1/F2; parallel with backend node work; M)

Implement list/detail/status filters, link-code form, rename/revoke dialogs, model state progression and setup links. Label GPU total/free VRAM as “Reported”; label benchmark capacity/throughput as “Tested” with last measurement time. Show rank timestamp and a provisional/unavailable state; do not suggest rank establishes confidentiality or hardware identity. Display earnings using billing fields and explain pending/held states using published reasons only. Revoke confirmation describes interrupted requests and then refetches server truth. Download/setup actions use released metadata, with accessible platform selection.

**Done when:** `npm run test:browser -- nodes` covers link approval, enrollment expiry, no nodes, offline/draining/warming/quarantined states, rename, revoke and unknown node; backend M2 test node appears and updates without page reload.

### F4 — Build API key management (M3; F1/F2; parallel with ledger work; M)

Build list/create/revoke with model and permission selection, exact XMR daily-cap input and server validation. Restrict available scope choices to the contract registry; defaults must come from shared policy or server configuration. Require explicit privacy notice acknowledgement before first key creation. Reveal newly returned secret once in a dismissible view; copy only on user click, provide manual selection fallback and never silently write to clipboard. Clear secret on dismissal/navigation/logout; do not retain it in a global store, localStorage, analytics, logs or error reports. Last-used null displays “Never used.”

**Done when:** `npm run test:unit -- keys && npm run test:browser -- keys` passes create/reveal/copy/revoke, invalid caps, 403/429 cases and duplicate-submit protection; a DOM/cache/storage scan after closing reveal finds no key secret.

### F5 — Build billing, payouts and security setup (M3/M4B; F1/F2; parallel page fixtures first; L)

Format all XMR amounts by string/BigInt operations with up to 12 decimal places and no binary float conversion. Show available, reserved, pending earnings, matured earnings and payout-reserved separately using C1 labels. Implement deposit address, network badge, verified QR payment URI, copy feedback, confirmation progress and paginated deposit/ledger history. Never rotate or regenerate deposit addresses in the browser; fetch the server's stable address. Show pending/locked/reversed deposits and explain delays.

Implement TOTP setup/confirmation and one-use step-up in the payout address form. Treat address validity/network as server-authoritative; client validation assists only. Show configured threshold/batch schedule/address cooldown and explicit unknown payout “Under reconciliation” with existing txids. Avoid a manual “retry payment” action. A successful request refreshes authoritative settings; failed or unknown mutations do not optimistically change money/address data. Include full reservation, cancellation, node-failure refund and fee-policy docs.

**Done when:** `npm run test:unit -- money && npm run test:browser -- billing` passes values above 2^53, zero/one-piconero/large balances, 9→10 confirmations, reversal, reserved funds, TOTP setup, step-up reuse rejection, address cooldown and unknown payout; stagenet browser workflow agrees with the backend ledger.

### F6 — Build analytics and complete setup docs (M4/M5; F2 plus read contracts; M)

Implement date/model filters in validated query parameters, summary metrics, request history and latency chart/table. Label time ranges UTC; local timestamps must include zone labels. Distinguish zero from null, no data from load errors, failures from successful requests and refunds from spend. Paginate usage; do not fetch unbounded historical rows to recompute authoritative totals. Provide a table equivalent for every chart and reduce animation under reduced-motion preference.

Write the docs routes with working OpenAI curl/TypeScript examples, supported subset, async warming errors, SSE failure/status lookup, proxy DNS and all supported platform install paths. Include the shared exact plaintext warning at first-use touchpoints. Verify examples using generated contract fixtures and the real M2 node; use placeholder secrets clearly labeled as such.

**Done when:** `npm run test:browser -- analytics && npm run test:docs` confirms filtered totals match a seeded server dataset, charts have table equivalents, docs links resolve and every API/setup command succeeds against the appropriate gated environment.

### F7 — Qualify product accessibility and release (M5/M6; F3–F6 + shared integration gates; M)

Run unit/browser gates with real nginx/Go services and fixture-backed failure scenarios. Test Chromium, Firefox and WebKit at 360px and desktop widths; include keyboard-only interaction, reduced motion and light/dark themes. Run automated accessibility checks on every route plus manual screen-reader/keyboard inspection of dialogs, tables, errors, secret reveal and live-status notices. Verify the production static build contains no environment secrets and private data is never prerendered.

**Done when:** `npm run check && npm run lint && npm run test:unit && npm run test:browser && npm run build` passes; no serious/critical automated accessibility violations remain; all release browser scenarios pass through nginx with generated contracts and documented limitations.

## Core 4 — Assumptions

- Adopt shared A01–A19 and D01–D11 unchanged; use the catalog and privacy defaults from PLAN rather than asking for product decisions.
- Use an accessible native CSS/token component system; add a component library only if a required interaction cannot be implemented/verified efficiently, with a dependency justification.
- Do not persist account API caches across browser sessions. Only non-sensitive theme preference and validated display preferences may use localStorage.
- Use English, responsive mobile/desktop UI and own local assets. Prefer text status labels over icon-only controls.
- Dates/filter bounds and economic values come from server DTOs; formatting is presentation only.
- A frontend route guard improves UX; server ownership checks are the authorization boundary.
- M0/M1 fixtures are explicitly marked demo/test data; replace mocks in integration/release tests before claiming a live flow works.

## Core 5 — Decisions

Execute [PLAN Decision log](PLAN.md#decision-log). Static SvelteKit, local state/resource cache and memory-only credential reveal are resolved frontend defaults. A contract discrepancy is fixed in PLAN and backend schemas first, then generated types; do not paper it over in `client.ts`.

## Security and privacy execution

Apply [PLAN Security 1–6](PLAN.md#security-1--threat-model) and C2/C4; frontend proof is owned by F1/F4/F5/F7. Render model/node/key names as plain text; never use `{@html}` for API data. Load fonts/assets locally. Use a CSP compatible with the actual build, no third-party trackers and no payload-capturing error SDK. Forms do not send secrets in URLs. Frontend logs never serialize request/response objects. Keep recovery codes/TOTP setup URI in the reveal component only; tear down its state after success/navigation.

Live sockets are invalidation transport; no client-supplied account filter may change tenant scope. Avoid cache races by attaching account generation and resource version to every request; ignore responses from an obsolete session or earlier revision. Present server incidents and financial uncertainty as explicit status, with support request IDs and docs links. Do not label a pending refund as completed until the server returns terminal state.

## Quality and design system

Create CSS tokens for background/surface/border/text/muted/accent/success/warning/danger, 4/8/12/16/24/32px spacing and two restrained type scales. Use high-contrast text, visible focus rings and tabular figures for amounts. Use semantic buttons/links, labels, described errors and heading order. Ensure ≥44px primary touch targets, readable long addresses, horizontal table overflow without whole-page overflow and status information independent of color. Avoid unnecessary motion; honor reduced motion and keep live updates from stealing focus.

Use dialogs with focus trap, initial focus, Escape dismissal where safe and focus return. A destructive revoke/payout address submit has a clear label and prevent-double-submit state. Announce action outcomes with polite live regions, not every heartbeat; use assertive announcements only for urgent auth/action errors. Respect password managers; use appropriate autocomplete values. Tab/clipboard interactions must work without exposing secret values to telemetry.

Unit tests target exact money parsing/formatting, UTC filter boundaries, state-generation fencing, API error handling, mutation state and WS reconnection/dedupe. Component tests cover labels, focus and state transitions. Playwright proves full user workflows and cross-account isolation using the backend's deterministic seed command. Use `npm run test:browser` against a fresh isolated Compose test project, not a funded account. Do not replace server integration assertions with screenshots alone.

Keep authored modules cohesive and around 300–400 lines as PLAN Quality specifies. UI depends on generated contract/domain/client modules; domain formatting never imports Svelte or browser state. Generated files remain untouched and reproducible.

## First-run verification and recovery

F0 verifies package compatibility and actual fixtures before route code. F1/F2 verify real cookies/CSRF/WS through nginx. F3/F5/F6 verify actual node/billing/docs data before final copy. F7 builds from a clean clone, validates deep-link refresh and scans built JS/HTML/storage/network logs for credential/privacy sentinels. Every acceptance command is a script to implement, not evidence of present execution.

Before running browser tests, validate `PUBLIC_ORIGIN`, generated types, safe test credentials, stagenet badge, fresh test seed and service readiness. Fail loudly on missing origin/schema and use a one-command scenario reproduction. After two failed fixes, inspect the server contract or browser network sample and update the shared assumption; do not introduce alternate JSON shapes. Retry safe reads with bounded backoff; ask the user to submit a failed mutation again only after authoritative status is known.

## Next actions

- **FN1 (F0, done 2026-10-09):** Scaffold locked static SvelteKit and generated API imports; DTO types and boundary validators are generated from shared contracts. **Verified:** `npm ci`, `npm run contracts:check`, `npm run check`, `npm test`, `npm run build`, and `python scripts/contracts-check.py` pass.
- **FN2 (F1):** Account session/navigation and privacy/recovery-code flow are implemented. Fixture-backed Playwright auth flows pass locally; the M1 stack browser scenario covers real registration, logout and cross-account isolation. Treat the M1 integration gate as complete only after GitHub Actions reports success.
- **FN3 (F2):** Typed account REST and ticket-authenticated C4 live client are implemented. Fixture-backed browser invalidation waits for the authoritative REST response; unit tests cover protocol, dedupe, resync and logout. The M1 stack browser scenario waits for actual SSE usage to appear through a live invalidation and REST refresh; its CI result is the completion gate.
- **FN4 (F3, partial):** The owner approval flow, paginated owner inventory, search/status filtering, detail, rename and revoke are implemented on the opt-in node API. Unmeasured metrics are nullable and UI labels distinguish reported hardware from tested capacity. Still needed: model commands, live node updates and the full M2 remote-node integration gate. **Verified locally:** Go ownership/pagination/CSRF/revoke/fencing tests; frontend type, 27 unit, 9 browser, generated-contract and production-build checks.

## Changelog

- v1, 2026-10-08: Define frontend ownership, routes, state architecture and F0–F7 execution gates.
- 2026-10-10: Add the authenticated one-time node enrollment approval screen and point the CLI verification URI to it.
- 2026-10-10: Add owner-only node inventory/detail/rename/revoke flows and nullable unmeasured Node fields; keep model controls and remote-node qualification open.

## Open risks

- Static app fallback/cookie handling can differ between dev server and nginx; real-edge browser tests are required.
- Browser extensions and clipboard managers can observe a deliberately revealed API secret; one-time display cannot prevent endpoint compromise.
- Live updates may lag or arrive twice; REST snapshots and explicit stale states must remain authoritative.
- Automatic accessibility tools miss reading-order/focus defects; manual keyboard and screen-reader checks remain required.
- Small XMR amounts and refunds are easy to misread; exact formatting and separate balance buckets are release-critical.
- Rank, tested capacity and privacy notices can create false trust if copy is vague; keep the limitations visible at setup and key creation.
