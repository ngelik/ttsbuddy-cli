# TTSBuddy CLI

Go CLI tool for converting text to speech via the TTSBuddy Agent TTS API.

## Quick Start

```bash
make build              # Build binary to bin/ttsbuddy
make test               # Run unit tests (deterministic, no network)
make test-live          # Run live API tests (requires TTSBUDDY_API_KEY)
make lint               # Run golangci-lint (install with: make tools)
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
```

## Key Patterns

- **cobra only, no viper** — one JSON config file doesn't justify viper
- **Pointer fields** on response structs — `*AudioInfo`, `*Billing`, `*float64` for absent optional data
- **`context.Context` everywhere** — per-request timeouts, SIGINT cancellation via `signal.NotifyContext`
- **Atomic downloads** — write to `.part`, rename on success, cleanup on error
- **Idempotency** — content-hash for file/arg inputs, UUID for stdin, `--idempotency-key` override

## API Contract

Source of truth: `docs/agent-tts-api-v1.md` and `docs/cli-implementation-guide.md` in the tts-study-buddy repo.

- POST `/functions/v1/agent-tts` — submit TTS, returns 200 (sync) or 202 (async)
- GET `/functions/v1/agent-tts?id=<job_id>` — poll status
- Auth: `Authorization: Bearer ttsb_<public_id>_<secret>`
- 11 error codes, all documented in `internal/api/types.go`

## Testing

- `make test` — unit tests only (httptest, no live API)
- `make test-live` — live API tests gated by `TTSBUDDY_TEST_LIVE=1`
- `scripts/integration_test.sh` — CLI smoke tests against live API
- `test_matrix.md` — exhaustive manual test checklist

## Release

- GoReleaser builds darwin/linux × amd64/arm64 on tag push
- Homebrew tap: `ngelik/homebrew-tap`
- `make release-snapshot` to test locally before tagging

## Dependencies

- `github.com/spf13/cobra@v1.8.1` — CLI framework
- `golang.org/x/term` — TTY detection
- Go stdlib for everything else
