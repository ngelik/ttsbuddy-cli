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
3. `make vuln` — zero reachable Go vulnerabilities

**Before every release (tag + push):**
1. `make test` — all unit tests pass
2. `make lint` — zero issues
3. `make vuln` — zero reachable Go vulnerabilities
4. `TTSBUDDY_API_KEY=ttsb_... make test-acceptance` — acceptance tests pass
5. Fix any failures and re-run until all acceptance tests are green
6. `make release-snapshot` — all platform builds and SBOMs succeed
7. Only then: `git tag -a vX.Y.Z` and `git push origin vX.Y.Z`

**Do NOT tag a release if acceptance tests have failures.** Rate-limit retries are acceptable, but any exit-code mismatch, missing output, or broken command is a blocker.

## Release

- GoReleaser builds darwin/linux × amd64/arm64 on tag push
- Homebrew tap: `ngelik/homebrew-tap`
- `make release-snapshot` to test locally before tagging
- GitHub Actions CI runs tests, lint, dependency review, CodeQL, and govulncheck
- GitHub Actions Release validates the tag, then publishes signed artifacts,
  checksums, SBOMs, and GitHub provenance attestations

## Linter Setup

`make tools` installs `golangci-lint`, `govulncheck`, `syft`, and
`goreleaser` to `$(go env GOPATH)/bin`. Ensure that directory is on your PATH:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

Then run: `make lint`. Config is in `.golangci.yml`.

## Dependencies

- `github.com/spf13/cobra` — CLI framework
- `golang.org/x/term` — TTY detection
- Go stdlib for everything else

## CLI authentication contract

- `internal/clerkfapi` owns the pinned native Clerk email-code protocol and its
  development probe gate; do not infer headers or response rotation.
- `internal/prompt` reads bounded email and hidden six-digit OTP input. Never
  accept the OTP through argv or print it.
- `ttsb_` is a permanent dashboard key; `ttsc_` is one replacing, seven-day
  CLI session with fixed Agent TTS scope and no refresh.
- Effective precedence is flag, environment, active CLI session, then permanent
  config. Auth status/logout always use the stored CLI session directly.
- Login/logout preserve `api_key`; failed remote logout preserves local state.
