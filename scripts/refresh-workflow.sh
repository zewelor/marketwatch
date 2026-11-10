#!/bin/sh
set -eu

# The updater reserves exit code 42 for symbol changes; all other errors fail CI.
error_log=$(mktemp)
trap 'rm -f "$error_log"' 0
status=0
"$1" 2>"$error_log" || status=$?
cat "$error_log" >&2

case "$status" in
  0)
    echo 'ready=true' >> "$GITHUB_OUTPUT"
    ;;
  42)
    echo 'ready=false' >> "$GITHUB_OUTPUT"
    echo '::warning::CoinLore symbol changes detected; catalog refresh paused. See summary.'
    {
      printf '### Catalog refresh paused\n\nCoinLore changed an existing symbol. The catalog was not updated.\n\n```\n'
      cat "$error_log"
      printf '```\n'
    } >> "$GITHUB_STEP_SUMMARY"
    ;;
  *)
    exit "$status"
    ;;
esac
