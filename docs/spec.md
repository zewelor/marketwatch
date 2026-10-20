# Behavior specification

Marketwatch performs one check: fetch CoinLore data, evaluate price and percentage-change rules, send
Pushover notifications and persist state. Scheduling is external to the program.
CLI options and examples are documented in the [README](../README.md).

## Configuration

The YAML root contains a nonempty `rules` list. Each rule has:

| Field | Requirement |
| --- | --- |
| `coin` | Case-insensitive symbol such as `btc`, or quoted positive numeric CoinLore ID |
| `threshold` | Positive, finite percentage movement in either direction |
| `above` | Positive, finite USD price; triggers at price >= value |
| `below` | Positive, finite USD price; triggers at price <= value |
| `window` | Required `24h` or `7d` with `threshold`; absent with `above`/`below` |

Each rule contains exactly one of `threshold`, `above` or `below`. Null, missing,
combined or invalid conditions are errors. Price rules do not measure a change in USD.

All rules are validated before market/notification requests or state writes. Unknown fields,
duplicate keys or canonical rule definitions, multiple YAML documents, invalid
values and unsupported windows are errors. Symbols resolve through the embedded
CoinLore catalog before I/O. Unknown or ambiguous symbols fail with an explicit
error; ambiguous symbols require a quoted numeric ID. CoinLore IDs are normalized
by removing leading zeros and deduplicated before fetching. Coin existence is
determined by the ticker response. `kind` and rule `id` are unsupported.
The catalog is updated from CoinLore `/api/assets/` with `just update-coins` or
the weekly/manual GitHub Actions workflow, which proposes changes in one PR.
Checks do not fetch a catalog or silently select the highest-ranked duplicate symbol.

Each rule's internal ID is the full hexadecimal SHA-256 fingerprint of its
canonical definition. Equivalent numeric spellings and coin aliases identify the
same rule. Identical definitions are rejected before I/O; multiple different
thresholds for one coin and condition are valid independent rules.

Currency is fixed to USD. CoinLore endpoints, timeouts and notification channel
are fixed in code. PostgreSQL 18 or newer is required. `DB_HOST`, `DB_NAME`,
`DB_USER` and `DB_PASSWORD` are required for both real checks and dry-runs;
`DB_PORT` defaults to `5432`, and `DB_SSLMODE` defaults to `require` and accepts
`disable`, `require`, `verify-ca` or `verify-full`. The database connection must
use a direct read/write endpoint, not a transaction-mode connection pooler.
Non-dry-run checks also require both Pushover credential variables.

CLI takes precedence over ENV, then defaults. `--state` is unsupported and is an
unknown flag; setting the legacy `MARKETWATCH_STATE` environment variable is an
explicit error. Empty paths, invalid booleans and invalid database settings are
errors before database or market/notification requests. A valid explicit flag overrides an invalid
value for the same option in ENV.

All market configuration is validated before database or market/notification requests. Dry-run
reads configuration and state in a read-only database transaction, fetches data
and logs decisions. If the state table is absent, it previews empty state. It does
not require Pushover credentials, send notifications or Healthchecks pings, acquire an advisory lock,
create tables or write state.

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
The key is the rule's full fingerprint. The fingerprint includes source, currency,
canonical coin ID, condition semantics, threshold, window and margin.
Percentage-rule fingerprints remain unchanged when
adding price-rule support. YAML order, comments and leading zeros in IDs do not affect it.

Changing from old directional rules or changing a definition creates a new key:
the old rule is removed, discarding its pending with a warning, and the new
definition starts inactive. Removed rules are deleted with the same warning.
Reordering unchanged definitions preserves activity and pending.

Valid version-2 state with manually named keys is rekeyed by matching fingerprints
to configured definitions. Activity and the exact pending message and observation
time are preserved, including during a market outage. The transition is logged.
If multiple entries would map to one key, the check fails before market/notification HTTP and leaves
the database state unchanged. Dry-run previews rekeying without saving it.

The state table is `public.marketwatch_state` with one singleton row:
`id boolean PRIMARY KEY DEFAULT true CHECK (id), state jsonb NOT NULL`. A real check
creates the table and an initial empty state only when the table is absent. An
existing table without the singleton row, malformed or incomplete JSON state,
case-variant or unknown fields, invalid values or an unsupported version fails
without a reset. Version 1 is unsupported. The `state` JSON document remains
version 2. PostgreSQL `jsonb` normalizes duplicate identical keys, so duplicate-key
rejection applies to legacy file validation before import, not to database reads.

A real check holds a session-level advisory lock on its single database connection
for the entire check. The connection remains attached to the direct read/write
endpoint for the check and is not reconnected mid-run. External scheduling must
still prevent overlapping checks. Dry-run takes no advisory lock. The program does
not use transaction-mode pooling.

Each state save uses a short database transaction. Evaluations and pending
notifications are committed before sending. After each API acceptance, that
pending message is cleared and committed immediately; activity remains unchanged.
API acceptance means HTTP 200 with JSON `status=1`.
Database errors stop the check. There is no reconnect or retry within a run. If a
commit outcome is uncertain, the next check reloads the database state.

There is no exactly-once guarantee. API acceptance followed by a crash or failed
post-acceptance commit can result in a duplicate on the next check. Market history
is not retained.

The program never reads or automatically migrates a legacy JSON state file. An
operator moving from file state must back up the file and validate it with the
last file-state release's dry-run before importing its version-2 JSON into the
singleton row. This catches duplicate JSON keys before `jsonb` normalizes them.
The documented manual `psql` procedure is in the README. Do not start against a
new empty database while an existing file contains state, since first-run
initialization would otherwise create empty state.

## Healthchecks

`HEALTHCHECKS_PING_URL` optionally supplies the base URL of a Healthchecks check.
Unset or empty disables the integration. Once a real check starts, it sends GET
to the base URL with `/start` appended before configuration/state validation.
After all work, including state persistence and closing the database session, it
sends GET to the base URL on success or with `/fail` appended on any returned
error, including partial failures. CLI argument parsing errors do not start a
check. Trailing path slashes are removed before appending the event; query
parameters are preserved. Dry-run sends no Healthchecks requests.

Each ping has a fixed two-second timeout, with no retries or redirects. The final
ping has an independent context so cancellation or expiry of the check deadline
still permits reporting failure, taking up to two additional seconds. Invalid
URLs, transport/read errors and HTTP statuses other than 200 produce warnings
only; they do not stop price checking or affect its result. Ping URLs and response
bodies are never logged. See the [Healthchecks Pinging API](https://healthchecks.io/docs/http_api/).

## Execution and delivery

Market and notification HTTP requests are sequential with a 10-second timeout and
a 90-second check context deadline. SIGINT/SIGTERM cancel the check; the final
Healthchecks ping uses the separate deadline described above. Logs are JSON and include rule decisions,
skips, accepted notifications, replaced/expired pending and an execution summary.
Exit 0 means no errors; exit 1 includes partial failures and expired pending.

The Linux amd64/arm64 image uses a statically linked binary, CA certificates and
UID/GID 65532. Real checks require PostgreSQL state and Pushover credentials
supplied through environment variables. The container needs no persistent `/data`
directory; the YAML configuration is mounted read-only at `/config`. Operators
provide their own scheduling and deployment. CI runs tests against PostgreSQL 18
and builds. After a successful
push-to-main verification, a separate job with package-write permission publishes
Linux amd64/arm64 images to GHCR as `latest` and `sha-<full-commit-SHA>`, and records
the manifest digest. Other refs and pull requests do not publish. Main runs are
serialized through publication and cleanup.

Cleanup runs after successful publication, targets only this repository's package,
and protects `latest`. It keeps the 10 newest other tagged images and 10 standalone
untagged images with retained multi-arch children. Older commit tags are not
permanent archives. The package is made public for anonymous pulls. Deployment is
external. A separate weekly/manual catalog workflow creates update PRs; it does
not merge them or publish binaries or images.

For verification commands and coverage, see [failure scenarios](failure-scenarios.md).
