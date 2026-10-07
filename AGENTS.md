# Repository guide

- [docs/spec.md](docs/spec.md) is the sole behavior contract. Keep implementation
  small and explicit: use flat packages, follow existing conventions, and apply
  YAGNI. CoinLore is the only market data source and USD is fixed; do not add an
  automatic provider fallback.
- Fail on invalid or unexpected state. Do not reset corrupt state, and do not treat
  logs as proof that a notification was delivered. Preserve other contributors'
  changes. Commit, push, publish, or deploy only when the task explicitly
  authorizes that action.
- Keep credentials, secret files, and secret values out of source, logs, and test
  artifacts. Do not read real secret files.
- Keep runtime requirements in the README. Operators own scheduling and deployment;
  do not ship environment-specific deployment templates.
- Use Context7 when exact or version-sensitive behavior of an external library,
  API, or CLI matters. Check the repository's installed version first; make one
  focused query and reuse its library ID. If its documentation is unavailable or
  does not match the API, consult the producer's documentation and report the
  limitation.
- Verify dependency and tool versions in official sources. Pin GitHub Actions
  to full commit SHAs with version comments and container images to digests.
  Do not add dependency bots or release automation unless the
  task asks for them.
- Before an isolated implementation, list likely failures in
  [docs/failure-scenarios.md](docs/failure-scenarios.md). Do not add unit tests
  after writing code. Prefer meaningful end-to-end scenarios that leave
  reproducible artifacts; run focused checks during development and the full
  verification at the end. Repeat full verification only after new changes,
  failures or unresolved concerns. Reassess property-based, stateful/model-based,
  and formal methods when contracts, state transitions, or concurrency change;
  record the decision, benefit and cost in the failure-scenarios guide.
- Canonical commands: `just verify` (format, vet, race-enabled E2E, amd64/arm64
  binaries), `just test`, `just e2e`, `droast --no-roast Dockerfile`,
  `just show_dockerignore`, and `just image`. E2E artifacts go
  under `artifacts/e2e/`. Keep the root `.dockerignore`; after changing Docker
  inputs, run `just show_dockerignore` and inspect the filtered files and size.
- Use `git --no-pager` for automated Git inspection. Do not change the global
  pager setting.
