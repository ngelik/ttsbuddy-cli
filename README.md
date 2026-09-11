# TTSBuddy CLI

Convert text to speech from the command line using the [TTSBuddy](https://ttsbuddy.com) API.

## Install

### Homebrew (macOS/Linux)

```bash
brew install ngelik/tap/ttsbuddy
```

### Go

```bash
go install github.com/ngelik/ttsbuddy-cli/cmd/ttsbuddy@latest
```

### Binary download

Download from [GitHub Releases](https://github.com/ngelik/ttsbuddy-cli/releases) and place in your `$PATH`.

## Update

### Homebrew

```bash
brew upgrade ngelik/tap/ttsbuddy
```

### Go

```bash
go install github.com/ngelik/ttsbuddy-cli/cmd/ttsbuddy@latest
```

### Binary

Download the latest release from [GitHub Releases](https://github.com/ngelik/ttsbuddy-cli/releases/latest) and replace the existing binary in your `$PATH`.

### Check version

```bash
type -a ttsbuddy
ttsbuddy version
```

After installing or updating, confirm that the first `ttsbuddy` found on your
`PATH` is the binary you intended to use. An older binary installed earlier on
`PATH` can shadow a newer Homebrew installation. If multiple installs appear,
inspect them before changing `PATH` or removing anything; do not delete an
install whose origin is unclear.

## Quick Start

```bash
ttsbuddy auth browser
ttsbuddy speak "Hello from TTS Buddy"
ttsbuddy auth status
```

`ttsbuddy auth browser` can sign in an existing Clerk account or create a new
one through Clerk's hosted page. On a new account's first successful consent,
TTS Buddy initializes the standard Free plan before issuing the CLI session.
`ttsbuddy auth email` signs in an existing account; add `--signup` to create a
new account with email verification entirely in the terminal. The signup flow
initializes the same standard Free plan before issuing the CLI session. Clerk
owns identity creation and verification, while the CLI only submits the
verified session to TTS Buddy. Each method stores the same seven-day `ttsc_`
CLI session. A new login replaces the prior CLI session. There is no refresh;
login again after expiry.

`ttsbuddy auth login` is an alias for `ttsbuddy auth email`.

On v0.11.0, `ttsbuddy auth email` may show `Clerk request returned status 422`
immediately after the email prompt, before a code prompt, when Clerk reports
that no account was found for the identifier. Check the address, or run
`ttsbuddy auth email --signup` to create an account. In v0.11.1 and newer, the CLI
makes that guidance explicit only for Clerk's allowlisted
`form_identifier_not_found` response; other provider failures remain generic.

In v0.11.2 and newer, signup addresses rejected by Clerk's
`form_email_address_blocked` policy receive direct guidance to use a different,
non-disposable email address and rerun `ttsbuddy auth email --signup`.

The signup prompt is intentionally conditional: an address that is already
registered may still reach Clerk's verification prompt under strict
enumeration protection, or may be rejected later by the provider. That prompt
does not prove a new account was created. For an existing account, use either
`ttsbuddy auth email` or `ttsbuddy auth browser`; the CLI never turns a signup
attempt into an automatic login.

```bash
# 1. Set your API key (create one in Dashboard -> Settings)
ttsbuddy config set key ttsb_your_key_here

# 2. Convert text to speech
ttsbuddy speak "Hello, world!"

# 3. Listen to the output
afplay ttsbuddy-*.mp3   # macOS
```

## Try It Without Signing Up

The [no-signup CLI demo](https://www.ttsbuddy.com/cli) uses fixed sample files and pregenerated audio to prove the CLI workflow without exposing an unrestricted public TTS endpoint.

Run the same constrained demo locally:

```bash
./demo/cli-demo.sh
```

The script builds `ttsbuddy` when needed and uses only the public demo key and allowlisted sample inputs. See [`demo/README.md`](demo/README.md) for the exact boundary and manual commands.

## Authentication

Use `ttsbuddy auth browser` for interactive terminal work, or `ttsbuddy auth
email` when a browser is unavailable. Use `ttsbuddy auth email --signup` to
create a new account by entering the verification code from email. If the
signup needs legal acceptance, CAPTCHA, MFA, or another field, use browser
authentication instead; the CLI never fills those requirements implicitly.
If the address is already registered, run either `ttsbuddy auth email` or
`ttsbuddy auth browser`; provider
enumeration protection can defer that determination until verification, so a
signup verification prompt is not evidence of a new identity.
Use a permanent `ttsb_` key for CI and unattended automation. Login/logout
store the CLI session separately and never overwrite `api_key`. Effective
precedence is `--key` > `TTSBUDDY_API_KEY` > active CLI session > stored
permanent key.

In v0.11.3 and newer, generic signed-out, missing-credential, invalid-credential,
and expired-session messages point to both interactive methods: `ttsbuddy auth
email` or `ttsbuddy auth browser`.

In v0.12.1 and newer, MFA, client-trust, new-password, and any other pending
Clerk sign-in task are reported as `BROWSER_AUTH_REQUIRED` with
`human_action_required: true`; no CLI credential is issued until the hosted
browser flow completes. Known account-not-found responses recommend the
structured signup command, while existing-account conflicts recommend
structured login without `--signup`:

```bash
ttsbuddy auth email start --email <address> --signup --json
ttsbuddy auth email start --email <address> --json
```

For automation that can read an authorized mailbox, use the two-step email
contract (available in v0.12.0 and newer). The start command never accepts a
code on argv and returns one JSON document with an opaque, short-lived
challenge ID:

```bash
ttsbuddy auth email start --email operator@example.com --json
printf '%s\n' "$OTP" | ttsbuddy auth email verify \
  --challenge-id <challenge_id> --code-stdin --json
ttsbuddy auth email cancel --challenge-id <challenge_id> --json
```

The continuation is local to the selected config directory, expires after ten
minutes, and contains no email address, OTP, proof token, or provider response.
`requires_email_verification` tells an agent that mailbox authorization is the
next step; `human_action_required` is reserved for browser-only requirements
such as CAPTCHA, MFA, or legal acceptance. Invalid codes keep the challenge so
the same opaque ID can be retried, while terminal failures clear it.
Successful sign-in replaces the existing CLI session; the prepared result
includes this handoff boundary explicitly.

If a speak/web submission is interrupted or its transport outcome is
ambiguous, the JSON error includes `error.idempotency_key` and a same-input,
same-options retry command. Reuse that key with `--idempotency-key`; once a job
ID exists, use `ttsbuddy status <job_id>` instead of submitting a duplicate.
Rate-limit responses honor a valid body `retry_after_seconds` first, then a
valid `Retry-After` delta or HTTP date. Delays longer than the bounded automatic
retry policy are returned for explicit operator action rather than retried
early. Terminal failures and expiry use the same sanitized recovery fields and
never echo provider response text.

### Isolated config and session state (v0.11.5+)

For CI, tests, and parallel agent runs, set `TTSBUDDY_CONFIG_DIR` or pass the
global `--config-dir /absolute/path` flag. The selected directory contains the
config file, last-job record, CLI session, and mutation/login locks; the flag
takes precedence over the environment variable. Paths must be absolute and
non-root, and invalid, empty, whitespace-only, or regular-file paths fail
closed instead of falling back to `~/.ttsbuddy`.

```bash
TTSBUDDY_CONFIG_DIR="$(mktemp -d)" ttsbuddy voices --json
ttsbuddy --config-dir "$(mktemp -d)" config get key
```

See [`docs/development/config-isolation.md`](docs/development/config-isolation.md)
for the v0.11.5 release note and verification matrix.

`ttsbuddy auth logout` revokes the stored session before clearing it. A network
or server failure retains the local session so the command can be retried.
`--local-only` skips revocation and warns that server validity may continue
until the absolute seven-day expiry.

API keys are created in **Dashboard → Settings** at [ttsbuddy.com/dashboard](https://ttsbuddy.com/dashboard). The [API Keys guide](https://www.ttsbuddy.com/docs/developers/api-keys) covers creation, storage, and revocation. Keys use the format `ttsb_<public_id>_<secret>`.

**Three ways to provide your key** (in priority order):

1. **Flag**: `-k ttsb_...` (leaks to shell history — avoid in shared environments)
2. **Environment variable**: `export TTSBUDDY_API_KEY=ttsb_...`
3. **Config file**: `ttsbuddy config set key ttsb_...` (stored at `~/.ttsbuddy/config.json`)

For full details on creating and managing keys, see the [API Keys guide](https://ttsbuddy.com/docs/developers/api-keys).

## Commands

### speak

Convert text to speech. The main command.

```bash
# Inline text
ttsbuddy speak "Hello world"

# From file (.md files auto-preprocessed for better narration)
ttsbuddy speak -f article.md -v bf_emma -o article.mp3

# From stdin
cat notes.txt | ttsbuddy speak -

# Custom voice, language, and speed
ttsbuddy speak "Bonjour" -v st_m1 --language fr -s 0.9
ttsbuddy speak "こんにちは" -v st_f2 --language ja

# JSON output (for scripting)
ttsbuddy speak "Hello" --json

# Save locally and return the saved-file metadata as JSON
ttsbuddy speak "Hello" --output hello.mp3 --json

# Print URL without downloading
ttsbuddy speak "Hello" --no-download

# Raw MP3 to stdout (pipe to player)
ttsbuddy speak "Hello" -o - | afplay -
```

**Flags:**

| Flag | Description |
|------|-------------|
| `-f, --file <path>` | Read text from file |
| `-v, --voice <id>` | Voice ID (default: `st_m1`) |
| `-l, --language <code>` | Supertonic language code (default: `en`; examples: `fr`, `de`, `ja`, `ko`, `ar`) |
| `-s, --speed <n>` | Speed 0.5–1.5 (default: 1.2) |
| `-o, --output <path>` | Output file (`-` for stdout) |
| `--output-dir <dir>` | Directory for auto-named files (default: `.`) |
| `--timeout <duration>` | Poll timeout (default: `10m`) |
| `--raw` | Skip markdown preprocessing |
| `--no-download` | Print audio URL instead of downloading |
| `--idempotency-key <key>` | Override auto-generated idempotency key |

**Notes:**
- `.md` and `.markdown` files are automatically preprocessed: headings, links, images, and code blocks are stripped for cleaner narration. Use `--raw` to send verbatim.
- Fast voices (`st_*`) support 30+ language modes through `--language`, use native display names in `ttsbuddy voices`, and support the full 0.5–1.5 speed range.
- Auto-named files use the pattern `ttsbuddy-YYYYMMDD-HHMMSS-<voice>.mp3`.

**Fast voice language codes:** `en`, `ar`, `bg`, `hr`, `cs`, `da`, `nl`, `et`, `fi`, `fr`, `de`, `el`, `hi`, `hu`, `id`, `it`, `ja`, `ko`, `lv`, `lt`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `es`, `sv`, `tr`, `uk`, `vi`.

### web

Convert a readable webpage to speech.

```bash
# Use backend account preferences for voice, language, and speed
ttsbuddy web https://www.ttsbuddy.com/docs/

# Translate the article to Russian with a Fast voice
ttsbuddy web https://www.ttsbuddy.com/docs/ --language ru --voice st_m1

# Print URL without downloading
ttsbuddy web https://www.ttsbuddy.com/docs/ --no-download
```

`web` fetches only `http` and `https` pages, extracts the readable article text locally, and sends the extracted text plus source URL to the API. If `--voice`, `--language`, or `--speed` are omitted, the backend applies your TTSBuddy account preferences. When the extracted article language differs from the target language, the backend translates the article before speech generation.

`web` supports the same output and polling flags as `speak`: `--voice`, `--language`, `--speed`, `--output`, `--output-dir`, `--timeout`, `--no-download`, and `--idempotency-key`.

During longer jobs, `web` shows the local extraction step, backend submission,
queued/processing status, and real provider percentages when the API has them.
When conversion completes, human output includes the job ID plus speech length,
MP3 size, and generation speed when available.

### voices

List available TTS voices.

```bash
# Curated list with Kokoro voices plus Supertonic language modes (always works offline)
ttsbuddy voices

# Full live catalog from API
ttsbuddy voices --all

# JSON output
ttsbuddy voices --json

# Discover voices without network access
ttsbuddy voices --language fr --json
ttsbuddy voices --engine supertonic --recommended --json
```

Voice output includes `ID`, native display `NAME`, `LANGUAGE`, language `CODE`, and `TYPE`; JSON also includes lowercase `engine`, `min_speed`, `max_speed`, and `recommended_speed` fields when authoritative metadata is available. `--language CODE` and `--engine ENGINE` are additive filters, and `--recommended` returns one deterministic match (preferring `st_m1`, then `af_heart`). The JSON shape remains an array, including an empty array when no voice matches. Supertonic Fast voices (`st_m1`-`st_m5`, `st_f1`-`st_f5`) appear once per supported language mode, for example `st_m1` appears as `Louis` under French with code `fr` and `Noah` under German with code `de`. If `--all` can't reach the live catalog, it falls back to the curated list with a warning on stderr.

The `--language`, `--engine`, and `--recommended` discovery options are
available in v0.12.0 and newer.

Kokoro voices use compact provider codes such as `a` for American English, `b` for British English, `f` for French, and `z` for Chinese. Fast voices use standard language codes from the list above.

### status

Check job status. **Read-only** — does not download audio.

```bash
# Check a specific job
ttsbuddy status <job_id>

# Check most recent job
ttsbuddy status

# Poll until complete
ttsbuddy status <job_id> --watch

# JSON output
ttsbuddy status <job_id> --json
```

### download

Download audio for an existing job without submitting new synthesis work. The
job ID is required so a failed or interrupted transfer can be retried without
duplicating generation. Processing jobs are polled using the configured
timeout, and downloads use the same URL allowlist, redirect checks, bounded
size, and atomic file handling as `speak`.

```bash
# Save to a chosen path
ttsbuddy download <job_id> --output audio.mp3

# Poll if needed and return the completed API response plus local file metadata
ttsbuddy download <job_id> --output audio.mp3 --json

# Auto-name within the configured output directory
ttsbuddy download <job_id>
```

The JSON `download` object is added only after a file is saved and contains an
absolute `path` plus the actual local `bytes` written. `--json` cannot be
combined with `--output -`; use `--output -` without JSON when a raw MP3 stream
is required. An interrupted or failed transfer returns an executable download
recovery action when the job and output path are known.

### config

Show or set configuration.

```bash
# Show all config values
ttsbuddy config

# Get a specific value
ttsbuddy config get voice

# Set values
ttsbuddy config set key ttsb_...
ttsbuddy config set voice st_m1
ttsbuddy config set language fr
ttsbuddy config set speed 0.9
ttsbuddy config set timeout 5m
ttsbuddy config set allow_custom_api_url true
```

Valid keys: `key`, `api_key`, `voice`, `language`, `default_language`, `speed`, `timeout`, `output_dir`, `api_url`, `cli_auth_url`, `tts_api_base_url`, `allow_custom_api_url`. `api_key` aliases `key`, and `default_language` aliases `language`.

### doctor (v0.12.0+)

Inspect local readiness without changing configuration. The default is offline
and safe for signed-out machines; `--online` adds bounded, read-only
connectivity checks. JSON mode emits one report and exits nonzero when a
blocking readiness issue remains:

```bash
ttsbuddy doctor
ttsbuddy doctor --json
ttsbuddy doctor --online --json
```

Reports include check status and actionable `next_actions`. Credentials are
reported only by source/type and are never echoed.

### version

```bash
ttsbuddy version
ttsbuddy version --json
```

## Configuration

**File:** `~/.ttsbuddy/config.json` (0600 permissions in 0700 directory)

```json
{
  "api_key": "ttsb_...",
  "default_voice": "st_m1",
  "default_language": "en",
  "default_speed": 1.2,
  "output_dir": ".",
  "poll_timeout": "10m"
}
```

**Precedence:** flags > environment variables > config file > defaults

| Setting | Env Variable | Flag | Default |
|---------|-------------|------|---------|
| API key | `TTSBUDDY_API_KEY` | `-k` | — |
| CLI auth URL | `TTSBUDDY_CLI_AUTH_URL` | — | `https://www.ttsbuddy.com/v1/cli-auth` |
| Voice | `TTSBUDDY_VOICE` | `-v` | `st_m1` |
| Language | `TTSBUDDY_LANGUAGE` | `-l, --language` | `en` |
| Speed | `TTSBUDDY_SPEED` | `-s` | `1.2` |
| Output dir | `TTSBUDDY_OUTPUT_DIR` | `--output-dir` | `.` |
| Poll timeout | `TTSBUDDY_TIMEOUT` | `--timeout` | `10m` |
| API URL | `TTSBUDDY_API_URL` | — | (production) |
| Voice catalog API URL | `TTSBUDDY_TTS_API_BASE_URL` | — | `https://tts.api.prod.ttsbuddy.website` |
| Allow custom API URL | `TTSBUDDY_ALLOW_CUSTOM_API_URL` | — | `false` |

By default, credentialed commands may use the production TTSBuddy hosts or localhost development endpoints. Sending an API key to any other HTTPS API host requires explicit opt-in with `ttsbuddy config set allow_custom_api_url true` or `TTSBUDDY_ALLOW_CUSTOM_API_URL=true`.

Browser authentication uses the public production Clerk OAuth client ID built
into the source, including binaries produced by `go install`. Release builds
inject and verify the same public value (for example,
`make build CLERK_OAUTH_CLIENT_ID=...`). Local development may set
`TTSBUDDY_CLERK_OAUTH_CLIENT_ID`
and `TTSBUDDY_CLERK_OAUTH_ISSUER` only together with
`TTSBUDDY_ALLOW_CUSTOM_API_URL=true`. The callback always uses an ephemeral
`127.0.0.1` port and `/callback`.

Release builds fail closed unless the repository variable
`CLERK_OAUTH_CLIENT_ID` is configured. It must match the public production
client ID compiled into source. Browser and email authentication are available
in packaged, Homebrew, and `go install ...@latest` builds.

## Global Flags

These work on any command:

| Flag | Description |
|------|-------------|
| `-k, --key <key>` | API key (overrides config/env) |
| `--json` | JSON output to stdout only, no human output on stderr |
| `--quiet` | Suppress progress output |

## Output Modes

| Mode | stdout | stderr |
|------|--------|--------|
| Default `speak` | nothing (file saved to disk) | spinner, status, "Saved to ...", final stats |
| `--json` | JSON response without downloading (unless an explicit output file is supplied) | nothing |
| `--output <file> --json` | JSON response plus additive `download.path` and actual `download.bytes` | nothing |
| `-o -` | raw MP3 bytes | spinner (if TTY) |
| `--quiet` | nothing | suppresses progress; data output such as a `--no-download` audio URL remains |
| `--no-download` | nothing | audio URL and final stats |

`--json` and `-o -` are mutually exclusive (both write to stdout) — combining them exits with code 2.

Human progress output shows honest stages such as fetching, submitting, queued,
processing, finalizing, and downloading. Percentages appear only when the
backend receives real provider progress. Completed jobs show speech length, MP3
size when known, generation speed, and job ID.

Completion JSON retains the API's numeric duration and file-size fields and
adds separate provenance fields: `provider`, `estimated`, or `unknown` for
duration, and `content_length`, `estimated`, or `unknown` for file size. A
provider value means provider-reported metadata, not an independent local
measurement. The `download.bytes` value is the authoritative byte count for
the local file.

Example human output for a webpage conversion:

```text
Fetching webpage...
Extracted "Top announcements of AWS re:Invent 2025" (10793 chars)
Submitting webpage TTS request...
Queued job fe57968d...
Processing 42%... (1m47s)
https://tts-buddy-history-prod.s3.amazonaws.com/pro/example.mp3?...
Job ID: fe57968d-0958-4ccd-a3e1-d87a7972f01e
Speech length: 14m23s
MP3 size: 14.4 MB
Generation speed: 39 chars/sec
```

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Runtime or API error (auth, provider, download) |
| `2` | Usage or config error (bad flags, missing key, validation) |
| `130` | Interrupted (Ctrl+C) — job ID printed for resume |

## Pipe Examples

```bash
# Save and play immediately (macOS)
ttsbuddy speak "Hello" -o hello.mp3 && afplay hello.mp3

# Batch convert markdown files
for f in docs/*.md; do
  ttsbuddy speak -f "$f" -o "${f%.md}.mp3"
done

# Get audio URL for scripting
ttsbuddy speak "Hello" --no-download --json | jq -r '.audio_url'
```

## Rate Limits

| Limit | Value |
|-------|-------|
| POST requests per minute | 1 per API key |
| GET requests per minute | 30 per API key |
| Max text length | 500,000 characters |
| Monthly TTS minutes | Depends on plan |
| Audio URL lifetime | Temporary — download immediately |

For full API details, see the [API Reference](https://ttsbuddy.com/docs/developers/api-reference).

## Troubleshooting

| Error | Fix |
|-------|-----|
| "Invalid API key or CLI session" | Run either `ttsbuddy auth email` or `ttsbuddy auth browser`; for automation, set a permanent key with `ttsbuddy config set key <your-key>` |
| "Subscription inactive" | Reactivate at [ttsbuddy.com/billing](https://ttsbuddy.com/billing) |
| "Rate limited" | Wait and retry (automatic with backoff) |
| "Monthly minutes exhausted" | Upgrade plan or wait for reset |
| "No API access" | Your plan may not include API access |
| "Text too long" | Split input into chunks under 500k characters |
| Audio file not found | Files expire based on plan — re-generate |

## Development

### Running Tests

```bash
# Unit tests (deterministic, no network)
make test

# Unit tests with race detector
go test -race ./...

# Coverage report
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out        # per-function summary
go tool cover -html=coverage.out         # interactive HTML report

# Lint (install with: make tools)
make lint

# Reachable Go dependency and toolchain vulnerabilities
make vuln

# Live API acceptance tests
TTSBUDDY_API_KEY=ttsb_... make test-acceptance
```

### Test Architecture

- **Internal packages** (`internal/api`, `internal/config`, `internal/markdown`) use standard Go unit tests with `httptest` servers — no network or live API needed.
- **Command tests** (`cmd/`) use a **subprocess pattern** to safely test `os.Exit` paths and direct `os.Stdout/Stderr` writes. Each test re-invokes the test binary via `TestHelperProcess`, capturing real output and exit codes.
- **Acceptance tests** (`tests/acceptance_test.sh`) run the built binary against the live API, gated by `TTSBUDDY_API_KEY`.

### Build

```bash
make build                   # build to bin/ttsbuddy
make tools                   # install lint, vulnerability, SBOM, and release tools
make release-snapshot        # test release build for all platforms
make clean                   # remove bin/ and dist/
```

## Documentation

- [API Reference](https://ttsbuddy.com/docs/developers/api-reference) — full endpoint documentation
- [CLI Guide](https://ttsbuddy.com/docs/developers/cli) — detailed usage guide
- [API Keys](https://ttsbuddy.com/docs/developers/api-keys) — creating and managing keys

## License

MIT
