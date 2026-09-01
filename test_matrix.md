# TTSBuddy CLI — Test Matrix

Check each item after verifying. Run `make test` for unit tests, `make test-live` for live API tests.

## Input Sources
- [ ] `ttsbuddy speak "inline text"` — positional arg
- [ ] `ttsbuddy speak -f test.txt` — file input
- [ ] `ttsbuddy speak -f test.md` — markdown file (auto-stripped)
- [ ] `ttsbuddy speak -f test.md --raw` — markdown file (not stripped)
- [ ] `echo "hello" | ttsbuddy speak -` — stdin pipe
- [ ] `ttsbuddy speak` (no input, no pipe) — exit 2
- [ ] `ttsbuddy speak "text" -f file.txt` — ambiguous input → exit 2

## Output Modes
- [ ] Default: saves auto-named .mp3 file, prints path to stderr
- [ ] `-o output.mp3`: saves to specified path
- [ ] `-o -`: raw MP3 bytes to stdout, spinner on stderr if TTY
- [ ] `--json`: API JSON response to stdout, no stderr
- [ ] `--no-download`: prints audio URL to stderr
- [ ] `--quiet`: no progress output
- [ ] `--json -o -`: exit 2 (mutually exclusive)

## Request Parameters
- [ ] Default voice (`st_m1`) and speed (`1.2`)
- [ ] Custom voice: `-v bf_emma`
- [ ] Custom speed: `-s 0.8`
- [ ] Speed out of range: `-s 2.0` → exit 2
- [ ] `st_*` voice speed: `-v st_m1 -s 1.3` → requests 1.3 without capping
- [ ] `--timeout 30s`: custom poll timeout
- [ ] `--idempotency-key custom-key`: override auto-generated key
- [ ] Text > 500k chars → exit 2

## Response Paths (require live API)
- [ ] POST 200 completed (inline, short text)
- [ ] POST 202 processing → poll → completed
- [ ] POST 200 failed replay (idempotency)
- [ ] GET 200 processing
- [ ] GET 200 completed
- [ ] GET 200 failed
- [ ] GET/POST 200 expired (`FILE_EXPIRED`) — legacy backward-compat only; new jobs don't use file retention
- [ ] 401 `INVALID_KEY` — bad key
- [ ] 403 `INACTIVE_SUBSCRIPTION` — inactive sub
- [ ] 403 `NO_API_ACCESS` — no api_access tier
- [ ] 403 `USAGE_LIMIT_EXCEEDED` — exhausted quota
- [ ] 429 `RATE_LIMITED` — >20 req/min
- [ ] 400 `INVALID_REQUEST` — missing text
- [ ] 400 `TEXT_TOO_LONG` — >500k chars
- [ ] 502 `TTS_PROVIDER_ERROR` — upstream down
- [ ] 500 `INTERNAL_ERROR` — server error

## Retry Behavior
- [ ] Transport error: retries with same idempotency key
- [ ] 502 with "Use a new Idempotency-Key": retries with new key
- [ ] 500 with "Use a new Idempotency-Key": retries with new key
- [ ] 429: waits Retry-After seconds before retry
- [ ] Max retries (3) exceeded: shows final error
- [ ] Non-retryable (401, 403): no retry

## Cancellation
- [ ] Ctrl+C during POST: clean exit
- [ ] Ctrl+C during poll: prints job_id + resume command, exit 130
- [ ] Ctrl+C during download: .part file cleaned up

## Config Precedence
- [ ] Flag `-k` overrides env `TTSBUDDY_API_KEY`
- [ ] Env `TTSBUDDY_API_KEY` overrides config file `api_key`
- [ ] Config file overrides default
- [ ] Same for voice, speed, timeout

## Commands
- [ ] `AUTH_ONLY=1 BINARY=bin/ttsbuddy ./tests/acceptance_test.sh` — signed-out auth lifecycle passes in an isolated HOME without an API key or network
- [ ] `ttsbuddy auth --help` — lists login, status, logout
- [ ] `ttsbuddy auth status` (signed out) — no network, exit 1
- [ ] `ttsbuddy auth logout` (signed out) — idempotent, exit 0
- [ ] `ttsbuddy auth logout --local-only` — clears only a stored CLI session and never contacts the server
- [ ] Development-only `ttsbuddy auth login` — stores an expiring `ttsc_` session without printing the code, proof, or credential
- [ ] Development-only `ttsbuddy auth status` — reports remote usability and never returns the bearer credential
- [ ] Development-only `ttsbuddy auth logout` — confirms remote revocation before clearing local state; a failed revocation retains local state
- [ ] Development negatives — unknown account, incorrect/expired code, MFA, and pending Clerk task disclose no account/proof/credential material
- [ ] Exchange replay/concurrency — one proof produces at most one CLI credential
- [ ] Replacement — second fresh login revokes the first and reports replacement only after local save
- [ ] Lifecycle — login → speak → status → logout works; reuse after logout is rejected
- [ ] Gate matrix — unset/false/malformed flag, malformed allowlist, included/excluded verified user; GET/DELETE/Agent TTS remain unaffected
- [ ] Existing permanent `api_key` remains byte-for-byte unchanged through login/logout
- [ ] Temporary config directory/file modes are `0700`/`0600`; final local/server cleanup is confirmed
- [ ] `ttsbuddy voices` — offline curated list with Kokoro voices plus Supertonic Fast language modes, native names, and no raw `F1`/`M1` labels
- [ ] `ttsbuddy voices --all` — live catalog fetch; Supertonic alias rows such as `code=F1` and `voice_id=st_f1` still display native names
- [ ] `ttsbuddy voices --all` (upstream down) — fallback to curated + warning
- [ ] `ttsbuddy voices --json` — JSON array output
- [ ] `ttsbuddy status <id>` — single GET, read-only
- [ ] `ttsbuddy status` — uses last_job.json
- [ ] `ttsbuddy status` (no last job) — exit 2
- [ ] `ttsbuddy status <id> --watch` — polls until terminal
- [ ] `ttsbuddy status <id> --json` — JSON output
- [ ] `ttsbuddy status nonexistent-id` — 404 → exit 1
- [ ] `ttsbuddy config` — shows all values (redacted key)
- [ ] `ttsbuddy config get key` — redacted key
- [ ] `ttsbuddy config set key ttsb_...` — saves + confirms
- [ ] `ttsbuddy config set key badkey` — exit 2 (must start with ttsb_)
- [ ] `ttsbuddy config set speed 2.0` — exit 2 (out of range)
- [ ] `ttsbuddy config set bogus value` — exit 2 (unknown key)
- [ ] `ttsbuddy version` — version/commit/date/go
- [ ] `ttsbuddy version --json` — JSON output
- [ ] `ttsbuddy --version` — same as version command

## TTY Detection
- [ ] Spinner visible on interactive terminal
- [ ] Spinner disabled when stderr piped: `ttsbuddy speak "hello" 2>/dev/null`
- [ ] No ANSI codes in piped output

## Markdown Preprocessing
- [ ] `.md` file: headings stripped
- [ ] `.md` file: `[text](url)` → `text` (link text preserved)
- [ ] `.md` file: images removed entirely
- [ ] `.md` file: code fences removed
- [ ] `.md` file: bold/italic markers removed
- [ ] `.txt` file: no stripping applied
- [ ] `--raw` on `.md` file: no stripping

## File Operations
- [ ] Auto-named: `ttsbuddy-YYYYMMDD-HHMMSS-<voice>.mp3`
- [ ] Collision: appends `-2`, `-3` on existing file
- [ ] Atomic: .part file created during download, renamed on success
- [ ] Cleanup: .part file removed on download error
- [ ] Output dir doesn't exist → exit 2

## Config Security
- [ ] `~/.ttsbuddy/` created with 0700
- [ ] `config.json` written with 0600
- [ ] API key redacted in `config` and `config get key` output
- [ ] Full key shown only on `config set key`
