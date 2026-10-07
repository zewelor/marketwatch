set shell := ["sh", "-eu", "-c"]

fmt:
    gofmt -w cmd internal

test:
    go test ./... -count=1

e2e:
    mkdir -p artifacts/e2e
    MARKETWATCH_ARTIFACT_DIR="{{justfile_directory()}}/artifacts/e2e" go test -race ./... -count=1 -json > artifacts/e2e/results.jsonl

verify:
    test -z "$(gofmt -l cmd internal)"
    go vet ./...
    just e2e
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
    python3 scripts/update-coins.py
