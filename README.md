# Marketwatch

Marketwatch checks CoinLore cryptocurrency prices in USD and sends Pushover
alerts. Each run checks once and exits. PostgreSQL remembers active alerts and
pending deliveries between runs.

## Run

1. Copy [config/example.yaml](config/example.yaml) to `config.yaml` and set your
   thresholds.
2. Set `DB_HOST`, `DB_NAME`, `DB_USER` and `DB_PASSWORD` for a direct read/write
   connection to PostgreSQL 18+. The role needs to create the state table in
   `public` on the first real run and read and write its row afterwards.
3. Preview a check with Docker (Linux amd64/arm64):

```sh
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --env DB_HOST --env DB_NAME --env DB_USER --env DB_PASSWORD \
  --env DB_PORT --env DB_SSLMODE \
  --mount "type=bind,src=$PWD/config.yaml,dst=/config/config.yaml,readonly" \
  ghcr.io/zewelor/marketwatch:latest check --dry-run
```

`DB_HOST` must be reachable from inside the container. The default port is
`5432` and the default TLS mode is `require`. Use `DB_SSLMODE=disable` for a
local database without TLS.

To send alerts, pass `--env PUSHOVER_APP_TOKEN --env PUSHOVER_USER_KEY` to Docker
and remove `--dry-run`. Dry-run reads state without writing it or sending alerts.
Pass secrets through your environment. Optionally pass
`--env HEALTHCHECKS_PING_URL` to monitor scheduled runs.

Schedule `marketwatch check` without overlapping runs. Collect its JSON logs
and treat a nonzero exit as a failed check. Back up `public.marketwatch_state`
to preserve alert state across recovery.

With Go 1.27.4, run from source with
`go run ./cmd/marketwatch check --config config.yaml --dry-run`.

## Rules

```yaml
rules:
  - {coin: btc, threshold: 3, window: 24h}
  - {coin: btc, above: 86000}
  - {coin: btc, below: 80000}
```

`threshold` alerts on a rise or fall of at least that percentage over `24h` or
`7d`. `above` and `below` are inclusive USD limits. Use a quoted CoinLore ID
when a symbol is ambiguous.

After firing, a rule stays quiet until it rearms. Percentage rules rearm when
the absolute change falls below the threshold minus 0.5 percentage points,
or at zero for thresholds of 0.5% or less. Price rules rearm once the price
moves more than 0.5% of the threshold back past the triggering level. Failed
sends are retried on later checks and expire after three hours.

## Verify locally

```sh
docker compose -f compose.dev.yaml up --wait
MARKETWATCH_TEST_POSTGRES=1 DB_HOST=127.0.0.1 DB_PORT=15432 \
  DB_NAME=marketwatch_test DB_USER=marketwatch_test DB_PASSWORD=marketwatch_test \
  DB_SSLMODE=disable just verify
docker compose -f compose.dev.yaml down
```

Test logs and state snapshots are in `artifacts/e2e/`.
