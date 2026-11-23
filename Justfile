set shell := ["sh", "-eu", "-c"]

fmt:
    gofmt -w cmd internal scripts

test:
    #!/bin/sh
    set -eu
    # Keep the fixture locked until teardown, including across checkouts.
    exec 9>>/tmp/marketwatch-test.lock
    flock 9
    set -a
    . ./compose.dev.env
    set +a
    cleanup() {
      status=$?
      docker compose --env-file compose.dev.env -f compose.dev.yaml down || {
        teardown_status=$?
        echo "PostgreSQL fixture teardown failed (exit $teardown_status)" >&2
        if [ "$status" -eq 0 ]; then
          status=$teardown_status
        fi
      }
      exit "$status"
    }
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    docker compose --env-file compose.dev.env -f compose.dev.yaml up --wait --wait-timeout 60
    mkdir -p artifacts/e2e
    MARKETWATCH_ARTIFACT_DIR="{{justfile_directory()}}/artifacts/e2e" go test -race ./... -count=1 -json > artifacts/e2e/results.jsonl

verify:
    test -z "$(gofmt -l cmd internal scripts)"
    go vet ./...
    just test
    just build

build:
    mkdir -p bin
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/marketwatch-amd64 ./cmd/marketwatch
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/marketwatch-arm64 ./cmd/marketwatch

image:
    docker buildx build --platform linux/amd64 --load -t marketwatch:local .

show_dockerignore:
    #!/bin/sh
    set -eu
    LC_ALL=C
    export LC_ALL
    test_dir="$(mktemp -d)"
    trap 'rm -rf "$test_dir"' 0 1 2 3 15
    mkdir -p "$test_dir/context"
    docker build --file - --progress=quiet --output "type=local,dest=$test_dir/context" . >/dev/null <<-'EOF'
    FROM scratch
    COPY . /context
    EOF
    find "$test_dir/context/context" -type f -print |
      sed "s#^$test_dir/context/context/##" |
      sort > "$test_dir/included"
    cat "$test_dir/included"
    printf '\n%s\n' '---'
    printf 'Total files:\t%s\n' "$(wc -l < "$test_dir/included" | awk '{print $1}')"
    printf 'Total size:\t%s\n' "$(du -sh "$test_dir/context/context" | awk '{print $1}')"

# Refresh the embedded symbol catalog, then review the diff and rebuild.
update-coins:
    go run ./scripts
