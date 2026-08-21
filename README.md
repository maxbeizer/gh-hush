# gh-extension-template

A template for building [GitHub CLI](https://cli.github.com/) extensions in Go.

## Using this template

1. Create a new repo from this template:
   ```bash
   gh repo create my-org/gh-my-extension --template maxbeizer/gh-extension-template --private --clone
   cd gh-my-extension
   ```

2. Update these files:
   - **`go.mod`** — change the module path to your repo
   - **`Makefile`** — change `EXTENSION_NAME` to your extension name (without `gh-` prefix)
   - **`.goreleaser.yml`** — change `project_name` and `binary` to `gh-<your-name>`
   - **`main.go`** — change `Use` field and implement your commands

3. Verify everything works:
   ```bash
   make ci
   make install-local
   gh my-extension
   ```

## Development

```bash
make help          # see all targets
make build         # build binary
make test          # run tests
make ci            # build + vet + test-race
make install-local # install extension from checkout
make relink-local  # reinstall after changes
```

## Releasing

Tag a version to trigger a release build:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The GitHub Actions workflow uses [goreleaser](https://goreleaser.com/) to build binaries for darwin/linux (amd64/arm64) and create a GitHub release. Once released, users install with:

```bash
gh extension install my-org/gh-my-extension
```

## What's included

| File | Purpose |
|------|---------|
| `Makefile` | Build, test, lint, install targets |
| `.goreleaser.yml` | Cross-platform binary releases |
| `.github/workflows/release.yml` | Automated releases on tag push |
| `.github/workflows/ci.yml` | CI on push/PR to main |
| `main.go` | Minimal starter with cobra + signal handling |
| `.gitignore` | Go/editor/OS ignores |
