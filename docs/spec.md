# Behavior specification

Marketwatch performs one check: fetch CoinLore data, evaluate price and percentage-change rules, send
Pushover notifications and persist state. Scheduling is external to the program.
CLI options and examples are documented in the [README](../README.md).

## Configuration

The YAML root contains a nonempty `rules` list. Each rule has:

| Field | Requirement |
| --- | --- |
| `id` | Unique, stable, nonblank string of at most 128 characters |
| `coin` | Case-insensitive symbol such as `btc`, or quoted positive numeric CoinLore ID |
| `threshold` | Positive, finite percentage movement in either direction |
| `above` | Positive, finite USD price; triggers at price >= value |
| `below` | Positive, finite USD price; triggers at price <= value |
| `window` | Required `24h` or `7d` with `threshold`; absent with `above`/`below` |

Each rule contains exactly one of `threshold`, `above` or `below`. Null, missing,
combined or invalid conditions are errors. Price rules do not measure a change in USD.

All rules are validated before network requests or state writes. Unknown fields,
duplicate keys or rule IDs, multiple YAML documents, invalid values and unsupported
windows are errors. Symbols resolve through the embedded CoinLore catalog before
I/O. Unknown or ambiguous symbols fail with an explicit error; ambiguous symbols
require a quoted numeric ID. IDs are normalized by removing leading zeros and deduplicated before
fetching. Coin existence is determined by the ticker response. `kind` is unsupported.
The catalog is updated from CoinLore `/api/assets/` with `just update-coins` or
the weekly/manual GitHub Actions workflow, which proposes changes in one PR.
Checks do not fetch a catalog or silently select the highest-ranked duplicate symbol.

Currency is fixed to USD. Source, endpoints, timeouts and notification channel are
fixed in code. Non-dry-run checks require both Pushover credential variables.

CLI takes precedence over ENV, then defaults. Empty paths and invalid booleans are
errors before configuration reads. A valid explicit flag overrides an invalid
value for the same option in ENV.

Dry-run reads configuration and existing state, fetches data and logs decisions.
It does not require credentials, send notifications, acquire a lock, create
directories or write state. With a concurrent check, it previews an atomic state
snapshot rather than participating in the check's transaction.

## Market data

One request fetches all unique IDs from
`https://api.coinlore.net/api/ticker/?id={id1},{id2},...`.

| API field | Use |
| --- | --- |
| `price_usd` | Positive, finite USD price |
| `percent_change_24h` | Finite provider-reported 24h change |
| `percent_change_7d` | Finite provider-reported 7d change |

Values must be JSON strings that parse as finite `float64` numbers. Zero is a
valid percentage change. Results are matched by ID rather than array order.
Missing or duplicate requested IDs and invalid prices skip that coin's rules.
An invalid change skips only rules for its window; values are never replaced
with zero. Other valid rules continue, and the check reports failure.

HTTP failures, timeout or malformed JSON prevent new market evaluations.
Existing pending notifications can still be sent. There is no provider fallback.
Responses are limited to 4 MiB and redirects are not followed.

CoinLore's ticker does not include a price-update timestamp. Fetch time is the
local time the HTTP response was received and does not establish price freshness.
Percentage changes come from the provider; the program does not reconstruct
reference prices or historical timestamps.

## Rule episodes

A percentage rule triggers when the absolute provider percentage change is >= threshold:
`threshold: 3` matches both +3% and -3%. It rearms when absolute change is strictly
below threshold minus 0.5 percentage points. At thresholds <= 0.5 it rearms only
at exactly zero. Comparisons use `float64` without rounding. A sign reversal while
active does not create another episode without an observed rearm.

`above` and `below` price conditions are inclusive. An `above` rule rearms when
price is strictly below threshold minus 0.5% of threshold. A `below` rule rearms
when price is strictly above threshold plus 0.5% of threshold. For thresholds
86000 and 80000 USD this means rearm below 85570 and above 80400 USD respectively.
Price and percentage rules have independent episodes; invalid percentage fields
do not prevent valid price rules from evaluating.

A valid observation meeting an inactive rule creates a pending notification and
marks the rule active. This includes the first observation. English messages
say "condition observed" without claiming that a crossing was observed.
An active rule stays quiet until it rearms. Missing data leaves activity unchanged.
Checks sample current conditions; crossings between checks can be missed.

Activity and pending delivery are independent. Rearming does not delete a pending
notification. A new episode replaces an older pending notification with a warning;
the program does not promise delivery of every episode during an outage.

## Pending notifications

There is at most one pending message per rule, containing its original text and
observation time. The title is always `Marketwatch`. Messages include the source,
coin ID, USD price, condition, relevant change/window and fetch time. CoinLore
notifications have no attribution link.

At age >= 3 hours, pending expires with a warning and a failed check. Expiry does
not reset activity. Age is checked again immediately before sending. Each check
attempts each pending message at most once; retry happens in a later check,
including when current market data is unavailable.

Pushover uses normal priority. Success requires HTTP 200 and JSON `status=1` and
means API acceptance, not delivery or reading on a phone. Failures leave pending
intact. HTTP 4xx stops subsequent sends for that check; rule evaluation and the
pre-send state save have already completed. Tokens and raw error responses are
not logged.

## Persistent state

Version 2 contains `version` and a `rules` map keyed by rule ID. Each entry stores
`fingerprint`, `active` and optional `pending` with `observed_at` and `message`.
The fingerprint includes source, currency, canonical coin ID, condition semantics,
threshold, window and margin. Percentage-rule fingerprints remain unchanged when
adding price-rule support. YAML order, comments and leading zeros in IDs do not affect it.

Changing from old directional rules resets activity and discards old pending
messages with a warning, because the fingerprint changes. Changed definitions
reset only the affected rule and remove its pending with a
warning. Removed rules are deleted with the same warning. A new definition starts
inactive. Reordering unchanged definitions preserves activity and pending.

A missing state file starts empty. Unreadable, empty, malformed, incomplete or
unsupported state fails before HTTP; the file is never silently reset or migrated.
Version 1 is unsupported. Field names must match exactly and repeated JSON keys
are rejected.

A nonblocking exclusive lock on `<state-path>.lock` is held through the entire
check. The stable lock file is never unlinked. Storage must support `flock`, atomic
rename within a directory and file/directory synchronization.

Saving writes a temporary file in the same directory, syncs it, renames it over
the state file and syncs the directory. All evaluations and pending messages are
saved before sending. After each API acceptance, that pending is cleared and saved
immediately; activity remains unchanged. A save error stops further sends.

There is no exactly-once guarantee. API acceptance followed by a crash or failed
save can result in a duplicate on the next check. Market history is not retained.

## Execution and delivery

HTTP requests are sequential with a 10-second timeout and a 90-second check
context deadline. SIGINT/SIGTERM cancel requests. Logs are JSON and include rule decisions,
skips, accepted notifications, replaced/expired pending and an execution summary.
Exit 0 means no errors; exit 1 includes partial failures and expired pending.

The Linux amd64/arm64 image uses a statically linked binary, CA certificates and
UID/GID 65532. Real checks require a writable, persistent state directory and
Pushover credentials supplied through environment variables. Operators provide
their own scheduling and deployment. CI tests and builds; it does not publish
or deploy. A separate weekly/manual catalog workflow creates update PRs; it does
not merge them or publish binaries or images.

For verification commands and coverage, see [failure scenarios](failure-scenarios.md).
