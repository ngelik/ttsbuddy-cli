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

## Quick Start

```bash
# Set your API key (get one at ttsbuddy.com/billing)
ttsbuddy config set key ttsb_your_key_here

# Convert text to speech
ttsbuddy speak "Hello, world!"

# Convert a file
ttsbuddy speak -f article.md -v bf_emma -o article.mp3

# Pipe from another tool
cat notes.txt | ttsbuddy speak -

# List available voices
ttsbuddy voices
```

## Commands

| Command | Description |
|---------|-------------|
| `speak <text>` | Convert text to speech |
| `speak -f <file>` | Convert file to speech (.md files auto-preprocessed) |
| `speak -` | Read from stdin |
| `voices` | List curated voices (offline) |
| `voices --all` | Fetch full voice catalog from API |
| `status [job_id]` | Check job status (read-only) |
| `status --watch` | Poll until job completes |
| `config` | Show configuration |
| `config set <key> <value>` | Set a config value |
| `version` | Print version info |

## Configuration

Config file: `~/.ttsbuddy/config.json`

Settings can also be set via environment variables (`TTSBUDDY_API_KEY`, `TTSBUDDY_VOICE`, etc.) or flags.

Precedence: flags > env vars > config file > defaults.

## License

MIT
