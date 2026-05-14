# TTSBuddy CLI

Convert text to speech from the command line using the [TTSBuddy](https://ttsbuddy.com) API.

## Install

### Homebrew (macOS/Linux)

```bash
brew install ngelik/tap/ttsbuddy
```

### Go

```bash
go install github.com/ngelik/ttsbuddy-cli@latest
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
go install github.com/ngelik/ttsbuddy-cli@latest
```

### Binary

Download the latest release from [GitHub Releases](https://github.com/ngelik/ttsbuddy-cli/releases/latest) and replace the existing binary in your `$PATH`.

### Check version

```bash
ttsbuddy version
```

## Quick Start

```bash
# 1. Set your API key (get one at ttsbuddy.com/billing)
ttsbuddy config set key ttsb_your_key_here

# 2. Convert text to speech
ttsbuddy speak "Hello, world!"

# 3. Listen to the output
afplay ttsbuddy-*.mp3   # macOS
```

## Authentication

API keys are created in the [TTSBuddy dashboard](https://ttsbuddy.com/billing). Keys use the format `ttsb_<public_id>_<secret>`.

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
| `-s, --speed <n>` | Speed 0.5–1.5 (default: 1.0) |
| `-o, --output <path>` | Output file (`-` for stdout) |
| `--output-dir <dir>` | Directory for auto-named files (default: `.`) |
| `--timeout <duration>` | Poll timeout (default: `10m`) |
| `--raw` | Skip markdown preprocessing |
| `--no-download` | Print audio URL instead of downloading |
| `--idempotency-key <key>` | Override auto-generated idempotency key |

**Notes:**
- `.md` and `.markdown` files are automatically preprocessed: headings, links, images, and code blocks are stripped for cleaner narration. Use `--raw` to send verbatim.
- Fast voices (`st_*`) support 30+ language modes through `--language`, use native display names in `ttsbuddy voices`, and auto-cap speed at 1.0.
- Auto-named files use the pattern `ttsbuddy-YYYYMMDD-HHMMSS-<voice>.mp3`.

**Fast voice language codes:** `en`, `ar`, `bg`, `hr`, `cs`, `da`, `nl`, `et`, `fi`, `fr`, `de`, `el`, `hi`, `hu`, `id`, `it`, `ja`, `ko`, `lv`, `lt`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `es`, `sv`, `tr`, `uk`, `vi`.

### voices

List available TTS voices.

```bash
# Curated list with Kokoro voices plus Supertonic language modes (always works offline)
ttsbuddy voices

# Full live catalog from API
ttsbuddy voices --all

# JSON output
ttsbuddy voices --json
```

Voice output includes `ID`, native display `NAME`, `LANGUAGE`, language `CODE`, and `TYPE`. Supertonic Fast voices (`st_m1`-`st_m5`, `st_f1`-`st_f5`) appear once per supported language mode, for example `st_m1` appears as `Louis` under French with code `fr` and `Noah` under German with code `de`. If `--all` can't reach the live catalog, it falls back to the curated list with a warning.

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
```

Valid keys: `key`, `voice`, `language`, `speed`, `timeout`, `output_dir`, `api_url`, `tts_api_base_url`

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
  "default_speed": 1.0,
  "output_dir": ".",
  "poll_timeout": "10m"
}
```

**Precedence:** flags > environment variables > config file > defaults

| Setting | Env Variable | Flag | Default |
|---------|-------------|------|---------|
| API key | `TTSBUDDY_API_KEY` | `-k` | — |
| Voice | `TTSBUDDY_VOICE` | `-v` | `st_m1` |
| Language | `TTSBUDDY_LANGUAGE` | `-l, --language` | `en` |
| Speed | `TTSBUDDY_SPEED` | `-s` | `1.0` |
| Output dir | `TTSBUDDY_OUTPUT_DIR` | `--output-dir` | `.` |
| Poll timeout | `TTSBUDDY_TIMEOUT` | `--timeout` | `10m` |
| API URL | `TTSBUDDY_API_URL` | — | (production) |
| Voice catalog API URL | `TTSBUDDY_TTS_API_BASE_URL` | — | `https://tts.api.prod.ttsbuddy.website` |

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
| Default `speak` | nothing (file saved to disk) | spinner, status, "Saved to ..." |
| `--json` | JSON response | nothing |
| `-o -` | raw MP3 bytes | spinner (if TTY) |
| `--quiet` | nothing | nothing |
| `--no-download` | nothing | audio URL |

`--json` and `-o -` are mutually exclusive (both write to stdout) — combining them exits with code 2.

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Runtime or API error (auth, provider, download) |
| `2` | Usage or config error (bad flags, missing key, validation) |
| `130` | Interrupted (Ctrl+C) — job ID printed for resume |

## Pipe Examples

```bash
# Convert and play immediately (macOS)
ttsbuddy speak "Hello" -o - | afplay -

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
| "Invalid API key" | Run `ttsbuddy config set key <your-key>` |
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

# Live API integration tests (requires running API)
TTSBUDDY_API_KEY=ttsb_... TTSBUDDY_API_URL=http://localhost:54321/functions/v1/agent-tts \
  ./scripts/integration_test.sh
```

### Test Architecture

- **Internal packages** (`internal/api`, `internal/config`, `internal/markdown`) use standard Go unit tests with `httptest` servers — no network or live API needed.
- **Command tests** (`cmd/`) use a **subprocess pattern** to safely test `os.Exit` paths and direct `os.Stdout/Stderr` writes. Each test re-invokes the test binary via `TestHelperProcess`, capturing real output and exit codes.
- **Integration tests** (`scripts/integration_test.sh`) run the built binary against a live or local API, gated by `TTSBUDDY_API_KEY`.

### Build

```bash
make build                   # build to bin/ttsbuddy
make tools                   # install golangci-lint + goreleaser
make release-snapshot        # test release build for all platforms
make clean                   # remove bin/ and dist/
```

## Documentation

- [API Reference](https://ttsbuddy.com/docs/developers/api-reference) — full endpoint documentation
- [CLI Guide](https://ttsbuddy.com/docs/developers/cli) — detailed usage guide
- [API Keys](https://ttsbuddy.com/docs/developers/api-keys) — creating and managing keys

## License

MIT
