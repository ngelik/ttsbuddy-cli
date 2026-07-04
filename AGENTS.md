# TTSBuddy CLI

Go CLI tool for converting text to speech via the TTSBuddy Agent TTS API.

## Quick Start

```bash
make build              # Build binary to bin/ttsbuddy
make test               # Run unit tests (deterministic, no network)
make lint               # Run golangci-lint (requires GOPATH/bin on PATH)
make tools              # Install golangci-lint and goreleaser
make test-acceptance    # Run acceptance tests (requires TTSBUDDY_API_KEY)
make release-snapshot   # Build all platforms locally (install with: make tools)
```

## Architecture

```
cmd/                    # Cobra commands (speak, voices, status, config, version)
internal/
├── api/                # HTTP client, response types, retry, idempotency, voices
├── config/             # ~/.ttsbuddy/config.json, precedence resolution, last-job
├── markdown/           # .md → plain text stripping for TTS
└── display/            # TTY-aware spinner, error messages, JSON output
tests/
├── acceptance_test.sh  # Full acceptance test against live API
└── manual-acceptance-plan.md  # Test plan documentation
```

## Key Patterns

- **cobra only, no viper** — one JSON config file doesn't justify viper
- **Pointer fields** on response structs — `*AudioInfo`, `*Billing`, `*float64` for absent optional data
- **`context.Context` everywhere** — per-request timeouts, SIGINT cancellation via `signal.NotifyContext`
- **Atomic downloads** — unique temp files via `os.CreateTemp`, rename on success, cleanup on error
- **Idempotency** — content-hash for file/arg inputs, UUID for stdin, `--idempotency-key` override
- **Download security** — scheme+host allowlist, redirect validation, bounded size (500MB), symlink-safe temps
- **Credential protection** — HTTP to non-localhost hard-blocked, keys redacted in all output

## Branching

Always work directly on `main` unless the user explicitly asks for a feature branch, worktree, or separate branch name.

## API Contract

Source of truth: `docs/agent-tts-api-v1.md` and `docs/cli-implementation-guide.md` in the tts-study-buddy repo.

- POST `/v1/agent-tts` — submit TTS, returns 200 (sync) or 202 (async)
- GET `/v1/agent-tts?id=<job_id>` — poll status
- Production base URL: `https://ttsbuddy.com/v1/agent-tts`
- Auth: `Authorization: Bearer ttsb_<public_id>_<secret>`
- 11 error codes, all documented in `internal/api/types.go`

## Testing

### Unit tests
```bash
make test       # deterministic, no network, runs with -race
make lint       # golangci-lint v2 (needs GOPATH/bin on PATH, see below)
```

### Acceptance tests (live API)
```bash
TTSBUDDY_API_KEY=ttsb_... make test-acceptance
# or directly:
TTSBUDDY_API_KEY=ttsb_... BINARY=bin/ttsbuddy ./tests/acceptance_test.sh
```

The acceptance test runs ~50 tests in an isolated HOME, with rate-limit-aware pacing for POST tests (~4 min runtime with default 20s delay). See `tests/manual-acceptance-plan.md` for the full test matrix. Adjust pacing with `POST_DELAY=65` if hitting rate limits.

### Test with installed binary
```bash
TTSBUDDY_API_KEY=ttsb_... BINARY=ttsbuddy ./tests/acceptance_test.sh
```

## Pre-Commit / Pre-Release Checklist

**Before every commit:**
1. `make test` — all unit tests pass
2. `make lint` — zero issues

**Before every release (tag + push):**
1. `make test` — all unit tests pass
2. `make lint` — zero issues
3. `TTSBUDDY_API_KEY=ttsb_... make test-acceptance` — acceptance tests pass
4. Fix any failures and re-run until all acceptance tests are green
5. `make release-snapshot` — all platform builds succeed
6. Only then: `git tag -a vX.Y.Z` and `git push origin vX.Y.Z`

**Do NOT tag a release if acceptance tests have failures.** Rate-limit retries are acceptable, but any exit-code mismatch, missing output, or broken command is a blocker.

## Release

- GoReleaser builds darwin/linux × amd64/arm64 on tag push
- Homebrew tap: `ngelik/homebrew-tap`
- `make release-snapshot` to test locally before tagging
- GitHub Actions CI runs test + lint on every push
- GitHub Actions Release runs goreleaser on tag push

## Linter Setup

`make tools` installs `golangci-lint` and `goreleaser` to `$(go env GOPATH)/bin`. Ensure that directory is on your PATH:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

Then run: `make lint`. Config is in `.golangci.yml`.

## Dependencies

- `github.com/spf13/cobra` — CLI framework
- `golang.org/x/term` — TTY detection
- Go stdlib for everything else
