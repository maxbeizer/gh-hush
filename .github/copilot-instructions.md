# Copilot Instructions for gh-hush

## Project overview

`gh-hush` is a Go-based GitHub CLI extension for safe, explainable notification triage. It authenticates through `gh`, classifies unread notifications with a user-owned YAML policy, previews every decision, and only mutates approved targets.

## Development guidelines

- Follow idiomatic Go and the existing package structure.
- Preserve the preview-first, conservative safety behavior described in `README.md`.
- Never weaken revalidation, evidence-failure handling, or per-thread operation ordering.
- Keep authentication tokens, response bodies, and notification content out of diagnostics.
- Avoid new dependencies when the standard library or existing dependencies suffice.
- Add focused, deterministic tests for behavior changes; table-driven tests are preferred where useful.
- Run `gofmt -w .`, `go mod tidy`, and `make ci` before submitting changes.

## Commands

```bash
make build
make test
make ci
make lint
```

The extension is installed with `gh extension install maxbeizer/gh-hush` and invoked as `gh hush`.
