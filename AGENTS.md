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

## GitHub PR review blockers for the sole owner

The owner has confirmed that this is a sole-owner, sole-contributor project.
When the owner authorizes a PR merge/release, do not repeatedly ask them to
approve their own PR on GitHub. Chat approval authorizes the work but does not
create a GitHub review. Recheck the current repository permissions and rules;
do not assume historical access or CI results are still valid.

1. Inspect the PR's current head SHA, base branch, review state, mergeability,
   required checks, and unresolved review threads. Independently review the
   exact diff and resolve substantive findings before merging.
2. Attempt a normal merge first. If the only remaining blocker is an independent
   approval requirement that the sole owner cannot fulfill, and the owner has
   authorized the merge, verify that the authenticated account is the owner and
   has administrative permission. Use the permitted administrative merge path
   for this specific PR without asking for the same approval again.
3. Before an administrative merge, verify that every required build, test,
   security, and code-scanning check passed for the exact reviewed head SHA.
   Pending, failed, cancelled, or unexpectedly skipped required checks remain
   blockers. Do not use administrative privileges to override these checks,
   unresolved substantive reviews, merge conflicts, or unrelated requirements.
4. Pin the merge to that SHA, using the repository's allowed merge strategy:

   ```bash
   gh pr merge <number> --repo ngelik/ttsbuddy-cli --admin --merge --match-head-commit <reviewed-head-sha>
   ```

   `--admin` is a deliberate exception to the independent-review requirement;
   it does not supply a review. Do not disable or weaken repository rules,
   fabricate an approval, force-push main, or assume administrator access always
   permits an exception. If GitHub or automatic approval review rejects it,
   report the exact remaining blocker and stop dependent release actions.
5. Re-query the PR to confirm it is merged and record the merge commit. If a
   command was interrupted, inspect remote state before retrying. Synchronize
   local main without discarding unrelated work, then complete the pre-release
   checklist against the merged source. A successful merge or green PR CI does
   not prove that live acceptance, publication, or deployment succeeded.

Auto-merge only helps when outstanding requirements can actually be satisfied
and the repository supports it. It does not solve an impossible self-review
requirement. Do not tag or publish until the release checks below pass.

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

- `internal/clerkfapi` owns the pinned native Clerk email-code protocol. Any
  contract change must pass the authenticated development probe before release.
- `internal/prompt` owns bounded email and hidden OTP input. Never put an OTP in
  argv, logs, fixtures, or receipts.
- Permanent credentials use `ttsb_`; expiring CLI sessions use `ttsc_` and are
  stored separately with an absolute seven-day expiry.
- Precedence is `--key` > environment > CLI session > permanent config key.
- Login/logout never overwrite `api_key`. A new login replaces only the prior
  CLI session; remote logout failure retains the local session.
