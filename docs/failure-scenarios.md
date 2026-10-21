# E2E and failure scenarios

## Concise notification text

Before implementation, account for symbols and numeric coin selectors, above/below
prices, signed changes in both windows, and numeric IDs absent from the embedded
catalog. Resolve the display symbol from the existing catalog; an unknown numeric
ID must fail configuration rather than send an unidentified alert.
Messages contain only the symbol, USD price and condition (including the observed
signed change and window for percentage rules). Exclude provider names, IDs,
fetch timestamps and freshness disclaimers. Persisted pending text remains intact
for retries. Extend the existing PostgreSQL/HTTP E2E episodes with exact request
text and unknown-ID cases, retaining reproducible artifacts.

Property/model-based and formal methods add little value for this text and catalog
validation change compared with explicit E2E cases; state transitions, fingerprints
and concurrency remain unchanged.

## Optional Healthchecks pings

Before implementation, account for missing/empty or malformed ping URLs, trailing
slashes and query parameters, HTTP errors/redirects, connection failure, stalled
requests, and cancellation of the price check. A real check sends start before
validation and success only after all work completes; configuration, database,
market, partial-rule and notification failures send fail instead. The final ping
gets its own two-second deadline even when the check context is cancelled.
Healthchecks failures only warn, do not change the check result, and must not
prevent market evaluation, state persistence or Pushover sends. Never log the
ping URL, transport error containing it, or response body. Dry-run sends no pings.

Use the PostgreSQL-backed E2E harness and local HTTP fixtures to capture ordered
ping/market/notification events, failures, timeouts and dry-run state snapshots.
Exercise environment wiring with the real CLI and retain fixture-only artifacts.
Property/model-based tests would add a second model for a two-ping sequence
without changing rule or storage transitions; their maintenance cost is not
justified here. Formal methods remain deferred because concurrency and delivery
guarantees are unchanged.

## PostgreSQL 18+ persistence transition

Before implementation, account for missing/invalid DB settings, PostgreSQL older
than 18, connection failure, competing checks, a missing singleton row, malformed
or unsupported state, and write/commit failures before or after an external send.
A lost COMMIT response is an uncertain outcome: stop this check and read committed
state on the next invocation. Never reconnect and replay an in-memory snapshot.
Persist pending messages before sending and clear each only after API acceptance;
an accepted message whose acknowledgement was not committed can be duplicated.
Pending expiry/replacement means delivery is not guaranteed.

Dry-run must work against both a fresh database and existing state using read-only
SQL, without creating tables, rows, or taking the application advisory lock. It
requires DB settings but no Pushover credentials. Invalid configuration must fail
before DB or market I/O. A real run owns a session advisory lock for its entire
lifetime; short save transactions must not remain open during HTTP. A lost DB
session can release the lock while HTTP is still in flight, so the scheduler must
also prevent overlap. Do not claim exactly-once delivery or multiple-worker support.

CLI subprocess tests must preserve omitted optional DB settings. In particular,
an unset `DB_PORT` must exercise the supported default of 5432 rather than fail
in test setup; explicitly configured ports must continue to work.

Reuse the existing controlled-clock E2E sequences against a real disposable
PostgreSQL 18 instance, recording database snapshots instead of state files. Add
fault-injection coverage for storage failures, exclusive ownership, restart,
read-only preview, and uncertain commits. These tests exercise the changed
persistence boundary directly. Property/model-based tests could explore longer
interleavings but add a second behavioral model without a currently uncovered
rule transition; formal methods remain deferred because execution is one check
at a time and external delivery cannot be made atomic with a database commit.
Reassess if concurrent workers or delivery guarantees change.

User-facing behavior: [README.md](../README.md). The E2E harness runs the production
monitor against local HTTP fixtures and a disposable PostgreSQL 18 database; it
controls the clock for repeatable sequences. The suite requires explicit test
opt-in and refuses database cleanup outside the documented loopback test database.

Run `just e2e` to execute race-enabled E2E coverage and write artifacts under
`artifacts/e2e/`. The target creates `results.jsonl` and a directory per test;
each `step-XX.json` records the fixture response, request fields, JSON logs,
result, and database state when present. Run `just verify` for formatting, vet, the
same E2E suite, and static amd64/arm64 binaries. `just test` runs the Go suite
without the race detector or persistent E2E artifacts.

For container changes, run `droast --no-roast Dockerfile`,
`just show_dockerignore` and `just image`. Inspect the filtered Docker context.
Use the [README](../README.md) for a container preview and runtime setup;
building arm64 is not a runtime arm64 test.

| Area | Covered failure or behavior |
| --- | --- |
| Configuration | Invalid YAML, duplicate/unknown fields, explicit rule IDs, duplicate canonical definitions, invalid coins, conditions or windows fail before market/notification or state I/O. Optional Healthchecks pings still report the failed check. |
| Market data | One request deduplicates IDs and matches results by ID regardless of response order. Numeric fields must be JSON strings parsing as finite `float64`; prices must be positive. HTTP errors, timeout, cancellation, malformed JSON, missing/duplicate IDs, invalid price, and independently missing or invalid change fields are covered. Invalid price skips the coin; invalid change skips only its window. Responses are limited to 4 MiB and redirects are rejected. |
| Rules | Inclusive trigger limits, strict rearm margins, repeated episodes, ID canonicalization, rule ordering, and definition changes. |
| Pending delivery | Retry after send failure, retry while CoinLore is unavailable, rearm retaining pending, new episodes replacing pending, and expiry at three hours. |
| Pushover and writes | Normal priority; acceptance requires HTTP 200 and JSON `status=1`. Non-success responses retain pending; a 4xx stops further sends; state is saved before sending and after each accepted notification. A crash after remote acceptance but before the success save can cause a duplicate. |
| Durable state | Version 2 stores each rule's `Fingerprint`, `Active`, and optional `Pending{ObservedAt, Message}`. Invalid, unknown or case-variant keys and version 1 are rejected before market/notification HTTP; committed state remains unchanged. PostgreSQL JSONB validates JSON syntax and canonicalizes repeated keys before application reads. |
| PostgreSQL | Missing/invalid connection settings and authentication failure, exclusive session ownership, a missing singleton, session loss before/after a send, restart, and a lost response after a server-confirmed COMMIT. |
| CLI and dry-run | Real-binary argument and environment handling; obsolete file-state options fail explicitly; dry-run avoids DDL, application advisory locks, sends, and state changes. |
| Healthchecks | Ordered start/success/fail pings including partial/configuration/database failures; absent/empty URL and dry-run disable pings. HTTP errors, redirects, connection failures, malformed URLs and two-second timeouts only warn; cancellation still sends fail. |

Fixtures verify application behavior against controlled responses. They do not
prove current CoinLore, Pushover or Healthchecks behavior, delivery by any live service,
or PostgreSQL availability, permissions, TLS, backup/restore and scheduler behavior
in the operator's deployment. Those require separate runtime evidence.

Controlled-clock sequence E2E is the current state-transition method: it drives
the production runner through retry, rearm, replacement, expiry, restart, and
write-failure paths while checking logs and committed database state. Property-based or
model-based testing is deferred because these meaningful transitions and
failure cases are explicitly covered. Formal methods are deferred while state
has one active check guarded by a session advisory lock and delivery does not promise
exactly-once semantics. Reassess these choices when state, contracts, or
concurrency change.

## Symbol lookup and bidirectional percentage rules

Before implementation, account for unknown and ambiguous symbols, case variants,
canonical numeric IDs, catalog download failures or duplicate IDs, invalid windows,
obsolete `kind` fields, negative/nonfinite thresholds, and thresholds at or below
0.5 percentage points. Resolve symbols before state or market I/O; failed catalog
updates must preserve the previous snapshot. Catalog changes must not silently
choose a different coin for an ambiguous symbol.

A rule observes the absolute percentage change. Test both signed trigger limits,
the strict 0.5 percentage-point rearm boundary, a sign reversal while active,
rearm followed by a negative episode, pending retries and definition resets.
A sign reversal without an observed rearm remains the same episode. For thresholds
<= 0.5, rearm at exactly zero so these valid rules do not stay active forever.
The fingerprint must distinguish the new semantics from old directional rules.
Controlled-clock sequence E2E remains sufficient for these explicit transitions;
property/model-based testing could explore longer sequences but adds maintenance
without an uncovered transition here. Formal methods remain deferred: locking
and the delivery guarantees are unchanged.

## Weekly catalog refresh workflow

Before adding automation, account for download/schema failure, no changes,
concurrent scheduled/manual runs, an existing update PR, and disabled repository
PR creation. Refresh and validation run with read-only permissions; only the
separate PR job can write, and its commit is limited to `coins.json`. A failed
refresh must not publish a partial catalog. Use one stable update branch so later
runs update the same PR, and serialize runs. Without changes, create no PR.
GitHub-hosted permission and PR behavior requires a real run after publication;
local validation covers workflow structure and the existing updater/resolver.

## Price thresholds alongside percentage movement

Before implementation, account for missing/multiple condition fields, null,
zero, negative or nonfinite values, and any `window` on price rules. Each rule
must specify exactly one of `threshold` (percentage), `above` or `below` (USD).
Test inclusive price boundaries, strict 0.5% price rearm margins, initial alerts,
repeated checks, independent high/low/percentage episodes, invalid percentage
fields leaving valid price rules usable, and percentage-to-price definition
changes resetting only the changed rule. Existing percentage fingerprints must
remain stable. Sequence E2E covers the added transitions; property/model-based
and formal methods remain deferred because storage/concurrency are unchanged
and these explicit boundaries are covered without a new model to maintain.

Artifact paths must use portable test case names. Avoid YAML condition text in
subtest names: characters such as colon make GitHub artifact uploads fail even
when tests pass. Invalid-condition cases use numeric names, as other config cases do.

## GHCR publication and retention

Before implementation, account for fork/PR publication, failed verification or
push, cancelled/overlapping main runs, registry permission failure, private
package visibility, unsupported native binaries, broken multi-arch child
manifests, accidental cleanup of `latest` or other packages, and artifact-only
cleanup failures. Verification stays read-only; publication is a separate
push-to-main job with package writes. Main runs are serialized through publication
and cleanup. Cleanup runs only after a successful push, targets this repository's
package, protects `latest`, and retains 10 other tagged plus 10 standalone
untagged images; multi-arch children are handled together by the cleanup action.
Validate workflow structure locally and prove publish/cleanup permissions and
anonymous native pull on GHCR. Two comparable Actions runs are needed to prove
Buildx cache reuse. No new application state transitions or concurrency are
introduced; existing E2E is sufficient and model/formal methods remain deferred.

## Automatic rule fingerprints

Before implementation, account for rule reordering, symbol case and numeric-ID
aliases, equivalent numeric spellings, different thresholds on the same coin,
duplicate canonical definitions, changes to one rule, and legacy version-2 state
with manual keys. The full SHA-256 fingerprint includes source, currency,
canonical coin ID, condition, threshold, window and rearm margin; comments and
YAML spelling do not affect it. Generated IDs reuse this definition fingerprint;
no rule ID is accepted in YAML. Unchanged definitions must preserve independent
activity and pending messages across restart and reordering. Changed definitions
start a new rule; removed state and its pending are discarded with a warning.

Rekey valid legacy state by matching fingerprints before evaluation. Preserve
the exact pending message and observation time. If multiple entries would map
to the same key, fail before market/notification HTTP without overwriting the state; never choose
one pending message silently. Dry-run previews rekeying without writing.
Sequence E2E must cover migration, retry during market failure, dry-run,
ambiguous legacy state and independent multiple thresholds. Property/model-based
testing could explore more permutations, but these finite identity and migration
cases are covered explicitly; formal methods remain deferred because locking
and delivery guarantees are unchanged.
