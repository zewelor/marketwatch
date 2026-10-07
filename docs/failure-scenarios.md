# E2E and failure scenarios

Behavior contract: [spec.md](spec.md). The E2E harness runs the production
monitor against local HTTP fixtures and a temporary on-disk state file; it
controls the clock for repeatable sequences.

Run `just e2e` to execute race-enabled E2E coverage and write artifacts under
`artifacts/e2e/`. The target creates `results.jsonl` and a directory per test;
each `step-XX.json` records the fixture response, request fields, JSON logs,
result, and state file when present. Run `just verify` for formatting, vet, the
same E2E suite, and static amd64/arm64 binaries. `just test` runs the Go suite
without the race detector or persistent E2E artifacts.

For container changes, run `droast --no-roast Dockerfile`,
`just show_dockerignore` and `just image`. Inspect the filtered Docker context.
Use the [README](../README.md) for a container preview and runtime setup;
building arm64 is not a runtime arm64 test.

| Area | Covered failure or behavior |
| --- | --- |
| Configuration | Invalid YAML, duplicate/unknown fields, explicit rule IDs, duplicate canonical definitions, invalid coins, conditions or windows fail before network or state I/O. |
| Market data | HTTP errors, timeout, cancellation, malformed JSON, missing/duplicate IDs, invalid price, and independently missing or invalid change fields. |
| Rules | Inclusive trigger limits, strict rearm margins, repeated episodes, ID canonicalization, rule ordering, and definition changes. |
| Pending delivery | Retry after send failure, retry while CoinLore is unavailable, rearm retaining pending, new episodes replacing pending, and expiry at three hours. |
| Pushover and writes | Non-success responses retain pending; a 4xx stops further sends; state is saved before sending and after each accepted notification. A crash after remote acceptance but before the success save can cause a duplicate. |
| Durable state | Version 2 stores each rule's `Fingerprint`, `Active`, and optional `Pending{ObservedAt, Message}`. Invalid, unknown, duplicate, or case-variant keys and version 1 are rejected before HTTP; the file remains unchanged. |
| CLI and dry-run | Real-binary argument and environment handling; dry-run avoids locks, directory creation, sends, and state changes. |

Fixtures verify application behavior against controlled responses. They do not
prove current CoinLore or Pushover behavior, delivery by either live service,
or filesystem and locking behavior on the operator's storage. Those require separate
runtime evidence.

Controlled-clock sequence E2E is the current state-transition method: it drives
the production runner through retry, rearm, replacement, expiry, restart, and
write-failure paths while checking logs and persisted state. Property-based or
model-based testing is deferred because these meaningful transitions and
failure cases are explicitly covered. Formal methods are deferred while state
is local to one process guarded by `flock` and delivery does not promise
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
with manual keys. Generated IDs reuse the full existing definition fingerprint;
no rule ID is accepted in YAML. Unchanged definitions must preserve independent
activity and pending messages across restart and reordering. Changed definitions
start a new rule; removed state and its pending are discarded with a warning.

Rekey valid legacy state by matching fingerprints before evaluation. Preserve
the exact pending message and observation time. If multiple entries would map
to the same key, fail before HTTP without overwriting the state; never choose
one pending message silently. Dry-run previews rekeying without writing.
Sequence E2E must cover migration, retry during market failure, dry-run,
ambiguous legacy state and independent multiple thresholds. Property/model-based
testing could explore more permutations, but these finite identity and migration
cases are covered explicitly; formal methods remain deferred because locking
and delivery guarantees are unchanged.
