# Marketwatch

A small Go CLI that checks cryptocurrency prices and percentage changes from
CoinLore and sends alerts through Pushover. Persistent JSON state keeps alerts
quiet until a condition clears and triggers again.

Run it manually or schedule it with your own scheduler. Prices are in
USD; percentage-change rules use CoinLore's 24h or 7d values.

## Quick start

Requires Go 1.27.1. Edit the illustrative thresholds in
[config/example.yaml](config/example.yaml), then preview a check:

```sh
go run ./cmd/marketwatch check --config config/example.yaml --state data/state.json --dry-run
```

Dry-run fetches market data and logs decisions without sending alerts or creating
or changing state files. It does not require Pushover credentials.

For real alerts, set `PUSHOVER_APP_TOKEN` and `PUSHOVER_USER_KEY` in your
shell environment, then run:

```sh
go run ./cmd/marketwatch check --config config/example.yaml --state data/state.json --dry-run=false
```

CoinLore does not require an API key. Write `coin: btc` or `coin: eth`; symbols are
case-insensitive and resolved through a catalog embedded in the binary. Unknown
symbols fail explicitly. For ambiguous symbols, use the quoted CoinLore ID shown
in the error. Numeric IDs such as `"90"` remain supported.

Maintainers refresh the catalog from CoinLore `/api/assets/` with `just update-coins`
(Python 3 required), review the diff, and rebuild. Checks do not download the catalog.

The `Update coin catalog` GitHub Actions workflow runs on Mondays at 06:17 UTC
and can also be started manually. It creates or updates one PR when the catalog
differs from the default branch; changes require review and merging, then a rebuild.
Enable **Allow GitHub Actions to create and approve pull requests** under
**Settings → Actions → General → Workflow permissions**. The workflow uses only
the built-in `GITHUB_TOKEN`; approve pending CI runs on its PR before merging.
GitHub schedules can be delayed and may be disabled in inactive public repositories.

## Rules

```yaml
rules:
  - coin: btc
    threshold: 3
    window: 24h
  - coin: btc
    above: 86000
  - coin: btc
    below: 80000
```

This alerts for both a rise of at least 3% and a fall of at least 3% over 24 hours.
Use `7d` for the provider's seven-day change. `above` alerts when price is >=
86000 USD and `below` when price is <= 80000 USD, including equality. Each rule
specifies exactly one of `threshold`, `above` or `below`; price rules omit `window`.
There is no `kind` or `id` field.

Thresholds are positive numbers. Each rule gets an internal SHA-256 fingerprint
from its canonical definition. Reordering rules or writing `BTC`, `btc` or `"90"`
preserves their state. Identical definitions are rejected; different thresholds
for the same coin are independent rules. Changing a definition creates a new rule
and discards the old rule's state and pending notification with a warning. See the
[example configuration](config/example.yaml) and [behavior specification](docs/spec.md).

An alert fires when an inactive rule meets its condition, including the first
check. The rule rearms after its absolute change falls strictly below threshold minus
0.5 percentage points (below 2.5% for a 3% rule). Repeated checks within the same episode
stay quiet, including a sign reversal without an observed rearm. Thresholds at
or below 0.5% rearm at exactly zero.

Price rules use a rearm margin of 0.5% of their threshold: `above: 86000` rearms
below 85570 USD; `below: 80000` rearms above 80400 USD. Each rule keeps its own
episode, so high, low and percentage alerts can coexist.

A failed notification stays pending for up to three hours and is retried on the
next check. A newer episode replaces an older pending alert. Notifications
include the observation time; they may describe an earlier condition.

CoinLore does not provide a price-update timestamp in the ticker response.
The displayed fetch time does not confirm price freshness. Pushover acceptance
confirms API acceptance, not that a notification was read. A crash after acceptance
but before saving state can produce a duplicate.

## CLI and environment

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--config` | `MARKETWATCH_CONFIG` | `/config/config.yaml` |
| `--state` | `MARKETWATCH_STATE` | `/data/state.json` |
| `--dry-run` | `MARKETWATCH_DRY_RUN` | `false` |
| — | `PUSHOVER_APP_TOKEN` | Required for real alerts |
| — | `PUSHOVER_USER_KEY` | Required for real alerts |

Explicit flags override environment variables, which override defaults:

```sh
MARKETWATCH_CONFIG=config/example.yaml \
MARKETWATCH_STATE=data/state.json \
MARKETWATCH_DRY_RUN=true \
go run ./cmd/marketwatch check
```

Empty paths are errors. Boolean values accept `true`/`false`, `1`/`0`, `t`/`f`
and Go's uppercase variants; an empty boolean environment value means false.
`--dry-run=false` overrides an enabled environment value. Use
`marketwatch check --help` for all options.

Logs are JSON. Exit code 0 means the check completed without errors; code 1 also
covers partial provider failures, failed notifications and expired pending alerts.
Valid rules continue to run when another rule has invalid market data.

State format version 2 stores rule fingerprints, activity and pending messages.
Invalid or unsupported state, including version 1, is rejected without resetting
or overwriting the file. Do not remove the state or its lock while a check runs.

When upgrading from manually named rule IDs, remove `id` from the YAML and retain
the state file. Matching version-2 entries are rekeyed by fingerprint, preserving
activity and pending messages. Multiple entries mapping to one key fail explicitly
without overwriting the state. Dry-run previews this transition without saving it.

## Docker

Published images support Linux amd64 and arm64:

```sh
docker pull ghcr.io/zewelor/marketwatch:latest
```

Every successful push to `main` publishes `latest` and `sha-<full-commit-SHA>`.
The workflow summary records the image digest; use `@sha256:…` to pin an exact
image. After publication, cleanup preserves `latest`, the 10 newest other tagged
images and 10 standalone untagged images, including the platform manifests of
retained multi-arch images. Older commit tags can disappear through retention.
The package must have public visibility in GitHub Packages for anonymous pulls.

Build the local amd64 image with `just image`, or:

```sh
docker buildx build --platform linux/amd64 --load -t marketwatch:local .
```

Preview with a read-only configuration:

```sh
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=bind,src=$PWD/config,dst=/config,readonly" \
  marketwatch:local check --dry-run
```

The image runs as UID/GID 65532. For real alerts, mount a persistent `/data`
directory writable by that user, and pass both Pushover variables with `--env`.
The filesystem must support `flock`, atomic rename and directory synchronization.

## Runtime setup

`marketwatch check` runs once and exits. Scheduling and deployment belong to the
operator. For an unattended setup:

- Set the environment variables listed above and supply Pushover credentials
  through your environment's secret mechanism.
- Provide a readable YAML configuration and a writable, persistent state
  directory. Mount the whole directory, including the lock file and temporary
  files used for atomic saves; mounting only `state.json` is insufficient.
- Run hourly and avoid overlapping checks. A second check fails while the first
  holds the state lock. Failed notifications are retried by later checks and
  expire after three hours.
- Retain JSON logs and surface exit code 1 as a failed check. Verify state
  persistence and storage support for locking and synchronization in your runtime.

## Development

```sh
just verify
```

This runs formatting checks, `go vet`, E2E tests with the race detector, CLI smoke
tests and static Linux amd64/arm64 builds. `just test` runs tests without retaining
artifacts; `just e2e` keeps logs and state snapshots in `artifacts/e2e/`.

CI runs the Go checks alongside Dockerfile lint, builds images for both
architectures and uploads E2E artifacts. The catalog workflow opens update PRs;
After successful verification of a push to `main`, a separate job publishes
images to GHCR and cleans up older versions. Pull requests and other branches
do not publish. Main runs finish publication and cleanup before the next begins.
See [testing and failure scenarios](docs/failure-scenarios.md) for coverage and
additional container checks.

## References

- [CoinLore API](https://www.coinlore.com/cryptocurrency-data-api)
- [Pushover API](https://pushover.net/api)
