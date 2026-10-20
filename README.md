# Marketwatch

A small Go CLI that checks cryptocurrency prices and percentage changes from
CoinLore and sends alerts through Pushover. PostgreSQL stores JSON state so alerts
stay quiet until a condition clears and triggers again.

Run it manually or schedule it with your own scheduler. Prices are in
USD; percentage-change rules use CoinLore's 24h or 7d values.

## Quick start

Requires Go 1.27.1 and PostgreSQL 18 or newer. Set `DB_HOST`, `DB_NAME`, `DB_USER`
and `DB_PASSWORD` for a direct PostgreSQL read/write endpoint. `DB_PORT` defaults
to `5432`; `DB_SSLMODE` defaults to `require`. For a local database without TLS,
set `DB_SSLMODE=disable`. Edit the illustrative thresholds in
[config/example.yaml](config/example.yaml), set `DB_PASSWORD` through your secret
mechanism, then preview a check:

```sh
export DB_HOST=127.0.0.1 DB_NAME=marketwatch DB_USER=marketwatch DB_SSLMODE=disable
go run ./cmd/marketwatch check --config config/example.yaml --dry-run
```

Dry-run reads the existing state from PostgreSQL, or previews empty state if the state table is absent. It
still requires database configuration, but does not create or change database
state and does not require Pushover credentials.

For real alerts, set `PUSHOVER_APP_TOKEN` and `PUSHOVER_USER_KEY` in your
shell environment, then run:

```sh
go run ./cmd/marketwatch check --config config/example.yaml --dry-run=false
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
| `--dry-run` | `MARKETWATCH_DRY_RUN` | `false` |
| — | `DB_HOST` | Required |
| — | `DB_NAME` | Required |
| — | `DB_USER` | Required |
| — | `DB_PASSWORD` | Required |
| — | `DB_PORT` | `5432` |
| — | `DB_SSLMODE` | `require` |
| — | `PUSHOVER_APP_TOKEN` | Required for real checks |
| — | `PUSHOVER_USER_KEY` | Required for real checks |
| — | `HEALTHCHECKS_PING_URL` | Unset/empty disables Healthchecks |

Explicit flags override environment variables, which override defaults:

```sh
MARKETWATCH_CONFIG=config/example.yaml \
MARKETWATCH_DRY_RUN=true \
go run ./cmd/marketwatch check
```

`DB_SSLMODE` accepts `disable`, `require`, `verify-ca` or `verify-full`. The
`--state` flag is unknown, and setting the legacy `MARKETWATCH_STATE` environment
variable fails explicitly. Empty paths and invalid booleans are errors. Boolean
values accept `true`/`false`, `1`/`0`, `t`/`f` and Go's uppercase variants; an
empty boolean environment value means false. `--dry-run=false` overrides an
enabled environment value. Configuration and database settings are validated
before database, market or notification requests. Use `marketwatch check --help` for all options.

Optionally set `HEALTHCHECKS_PING_URL` to a check's base ping URL, including for
self-hosted Healthchecks. Each real check sends GET to `/start` before checking,
then to the base URL on success or `/fail` on any check error (including partial
failures). Each ping has a two-second timeout, without retries or redirects.
Healthchecks failures produce warnings and leave price checking and its exit code
unaffected. Dry-run sends no pings. The URL is treated as a secret and is not logged.

Logs are JSON. Exit code 0 means the check completed without errors; code 1 also
covers partial provider failures, failed notifications and expired pending alerts.
Valid rules continue to run when another rule has invalid market data.

The `public.marketwatch_state` table has one row: a boolean primary key constrained
to `true`, and a `jsonb` state value. The JSON document remains version 2 and stores
rule fingerprints, activity and pending messages. A real check creates the table
and its initial empty state only when the table is absent; a table with no singleton
row, invalid JSON state or an unsupported version fails without resetting it.
Database errors stop the check. A real check uses one database session for its
whole run and does not reconnect mid-check.

When upgrading from manually named rule IDs, remove `id` from the YAML and retain
the database state. Matching version-2 entries are rekeyed by fingerprint,
preserving activity and pending messages. Multiple entries mapping to one key fail
explicitly without overwriting the state. Dry-run previews this transition without
saving it.

### Migrating an existing JSON state file

The program never reads or imports the legacy state file. Before upgrading, back
up the state file and keep it until the database import has been checked. Import
the file before the first application run against the new database; otherwise that
run initializes an empty state row. Use a PostgreSQL 18+ `psql` client connected
to the same direct read/write endpoint. Set `PGHOST`, `PGPORT`, `PGDATABASE`,
`PGUSER` and `PGSSLMODE` from the corresponding `DB_*` settings, and provide the
password through a protected `.pgpass` file or the interactive prompt.

Before importing, validate the backup with the previous file-state release's
dry-run, for example:

```sh
marketwatch-legacy check --config config/example.yaml \
  --state /secure/backup/marketwatch-state.json --dry-run
```

Use the old binary for this validation. The current release rejects `--state`.
The old dry-run checks the JSON state without changing the file. This is required
because PostgreSQL `jsonb` normalizes duplicate identical keys when it stores JSON.

Create the table and insert the singleton in one transaction. The `psql` variable
reads the backup locally; `:'state'` quotes its contents as an SQL literal
([PostgreSQL 18 psql documentation](https://www.postgresql.org/docs/18/app-psql.html#APP-PSQL-INTERPOLATION)).

```sh
PGHOST="$DB_HOST" PGPORT="${DB_PORT:-5432}" PGDATABASE="$DB_NAME" \
  PGUSER="$DB_USER" PGSSLMODE="${DB_SSLMODE:-require}" \
  psql -X -v ON_ERROR_STOP=1 <<'SQL'
\set state `cat /secure/backup/marketwatch-state.json`
BEGIN;
CREATE TABLE public.marketwatch_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  state jsonb NOT NULL
);
INSERT INTO public.marketwatch_state (id, state)
VALUES (true, :'state'::jsonb);
SELECT id, state->>'version' AS version FROM public.marketwatch_state;
COMMIT;
SQL
```

The create statement intentionally fails if the destination table already exists,
and the insert does not overwrite a singleton. Verify that the query returns one
row with version `2` before running a real check, and retain the original backup.
Resolve any pre-existing table or row explicitly instead of overwriting it.

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
  --env DB_HOST --env DB_NAME --env DB_USER --env DB_PASSWORD \
  --env DB_PORT --env DB_SSLMODE \
  --mount "type=bind,src=$PWD/config,dst=/config,readonly" \
  marketwatch:local check --dry-run
```

The image runs as UID/GID 65532. Mount `/config` read-only and pass the required
`DB_*` variables for both real checks and dry-runs. State is stored in PostgreSQL;
`DB_HOST` must be reachable from inside the container. The container does not need
a writable `/data` directory. Pass both Pushover variables for real checks and
`--env HEALTHCHECKS_PING_URL` when enabling Healthchecks.

## Runtime setup

`marketwatch check` runs once and exits. Scheduling and deployment belong to the
operator. For an unattended setup:

- Set the environment variables listed above and supply Pushover credentials
  through your environment's secret mechanism.
- Provide a readable YAML configuration and PostgreSQL 18+ through a direct
  read/write endpoint, not a transaction-mode pooler. The connection must stay on
  the same database session for the whole check so its session advisory lock is
  held until the check finishes. The program does not reconnect mid-run.
- The database role must be able to create the state table in `public` on first
  use and read/update its singleton row on later checks.
- Run hourly and prevent scheduler overlap. Failed notifications are retried by
  later checks and expire after three hours.
- Retain JSON logs and surface exit code 1 as a failed check. Confirm database
  backups and recovery for the state table.

## Development

Tests require a disposable PostgreSQL 18+ database. Set
`MARKETWATCH_TEST_POSTGRES=1`, `DB_HOST` to a loopback address, `DB_NAME` to
`marketwatch_test`, and `DB_USER`/`DB_PASSWORD` to disposable credentials;
`DB_PORT` defaults to `5432`. Tests fail when the required database is unavailable
instead of skipping PostgreSQL coverage. For example, start a local test server:

```sh
docker run --rm --detach --name marketwatch-postgres-test \
  -e POSTGRES_USER=marketwatch_test \
  -e POSTGRES_PASSWORD=marketwatch_test \
  -e POSTGRES_DB=marketwatch_test \
  -p 127.0.0.1:15432:5432 \
  --tmpfs /var/lib/postgresql:rw \
  postgres:18@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336
```

Set `DB_HOST=127.0.0.1`, `DB_PORT=15432`, `DB_NAME=marketwatch_test`,
`DB_USER=marketwatch_test`, `DB_PASSWORD=marketwatch_test`, and
`DB_SSLMODE=disable` for this disposable local instance, then run:

```sh
MARKETWATCH_TEST_POSTGRES=1 just verify
```

This runs formatting checks, `go vet`, PostgreSQL-backed E2E tests with the race
detector, CLI smoke tests and static Linux amd64/arm64 builds. `just test` runs
tests without retaining artifacts; `just e2e` keeps logs and state snapshots in
`artifacts/e2e/`.

Stop and remove the disposable server after testing with
`docker stop marketwatch-postgres-test`.

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
