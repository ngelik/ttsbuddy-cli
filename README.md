# TTSBuddy CLI — text to speech from your terminal

Convert text, Markdown files, and webpages to MP3 with the [TTS Buddy](https://www.ttsbuddy.com) API. Use it interactively, in shell scripts, or with AI agents.

- Choose Kokoro voices or multilingual Supertonic Fast voices.
- Read inline text, files, or stdin; extract readable articles from webpages.
- Save audio locally, return JSON, and recover existing jobs without submitting them again.
- Sign in through a browser or email, or use credentials for automation.

This guide describes the [latest release](https://github.com/ngelik/ttsbuddy-cli/releases/latest). Audio generation uses the hosted TTS Buddy service and your account's plan allowance.

[CLI documentation](https://www.ttsbuddy.com/docs/developers/cli) · [Agent skill](skills/tts-buddy/SKILL.md) · [Try the demo](https://www.ttsbuddy.com/cli)

## Install and update

**Homebrew (macOS)**

```bash
brew install ngelik/tap/ttsbuddy
# Update an existing installation:
brew upgrade ngelik/tap/ttsbuddy
```

**Go** — install or update:

```bash
go install github.com/ngelik/ttsbuddy-cli/cmd/ttsbuddy@latest
```

Add your `GOBIN` directory (by default, `$(go env GOPATH)/bin`) to `PATH` if needed.

**Binaries** — download the [latest release](https://github.com/ngelik/ttsbuddy-cli/releases/latest) for macOS or Linux (Apple Silicon/ARM64 or x86-64), then put `ttsbuddy` on your `PATH`. Releases include checksums, signed checksum bundles, SBOMs, and GitHub provenance attestations.

Check the version and which installation your shell uses:

```bash
ttsbuddy version
type -a ttsbuddy
```

## Quick start

```bash
ttsbuddy auth browser
ttsbuddy speak "Hello from TTS Buddy" --output hello.mp3
```

Open `hello.mp3` in your audio player. Browser authentication supports sign-in and signup. For terminal email authentication, use `ttsbuddy auth email`, or `ttsbuddy auth email --signup` for a new account. Browser-only requirements such as MFA or CAPTCHA require the hosted flow.

For unattended scripts, create a permanent API key in [Dashboard → Settings](https://www.ttsbuddy.com/dashboard) and supply it through `TTSBUDDY_API_KEY` using your secret manager. See the [API Keys guide](https://www.ttsbuddy.com/docs/developers/api-keys).

Want to try the workflow first? The [no-signup demo](https://www.ttsbuddy.com/cli) uses fixed samples and pregenerated audio. Its [local script](demo/README.md) is included in this repository.

## Examples

```bash
# Narrate a Markdown file (formatting is cleaned up automatically)
ttsbuddy speak --file article.md --output article.mp3

# Read from a pipe
cat notes.txt | ttsbuddy speak - --output notes.mp3

# Find a French voice, then narrate French text
ttsbuddy voices --language fr --recommended --json
ttsbuddy speak "Bonjour" --voice st_m1 --language fr --speed 0.9 --output bonjour.mp3

# Narrate a webpage using your account's voice, language, and speed preferences
ttsbuddy web https://www.ttsbuddy.com/docs/ --output guide.mp3

# Save audio and return the response plus local file metadata as JSON
ttsbuddy speak "Build complete" --output result.mp3 --json

# Check or download an existing job without generating new audio
ttsbuddy status <job_id> --watch --json
ttsbuddy download <job_id> --output recovered.mp3 --json
```

For `speak` and `web`, `--json` alone returns the API response without downloading; add `--output` to save a file. `speak` uses CLI configuration, while `web` uses account preferences unless you pass `--voice`, `--language`, or `--speed`. `--language` selects a language mode for Supertonic (`st_*`) voices; choose a Kokoro voice for its native language.

## Commands

| Command | Purpose |
| --- | --- |
| `speak` | Convert text, a file, or stdin to speech |
| `web <url>` | Extract and narrate a readable webpage |
| `voices` | Browse the built-in voice list; `--all` requests the live catalog |
| `status [job_id]` | Inspect a job; omit the ID to use the last local job |
| `download <job_id>` | Save audio from an existing job, polling if needed |
| `auth` | Sign in, inspect the stored CLI session, or log out |
| `account delete` | Prepare, confirm, inspect or cancel permanent account deletion |
| `billing` | Inspect agent billing and explicitly execute an authorized upgrade |
| `config` | View or set local configuration |
| `doctor` | Check local readiness; `--online` adds connectivity checks |
| `completion` | Generate shell completions |
| `version` | Show the installed version |

Run `ttsbuddy <command> --help` for flags and usage. Use `--json` for structured output. Configuration defaults to `~/.ttsbuddy`; `--config-dir` or `TTSBUDDY_CONFIG_DIR` selects an isolated directory.

## AI agents and billing

Use the [TTS Buddy agent skill](skills/tts-buddy/SKILL.md) for authentication, voice selection, JSON contracts, safe retries, configuration, and billing recovery. Copy `skills/tts-buddy/` into your agent's skills directory, or give the agent the file directly.

For Hermes Agent, see the [native TTS Buddy plugin guide](plugins/hermes-ttsbuddy/README.md) for installation, profile setup, audio delivery, and job recovery.

Find the text-to-speech skill in these directories:

- [skills.sh](https://www.skills.sh/ngelik/ttsbuddy-cli/tts-buddy)
- [SkillMD.ai](https://skillmd.ai/skills/tts-buddy-text-to-speech-agent-skill/)

Agent access is issued through the owner-approved [Auth.md flow](https://www.ttsbuddy.com/auth.md). The CLI accepts those credentials but does not register or renew them. Structured email authentication is also available for authorized mailbox workflows; see the skill for the start/verify protocol.

**Login approval is not spending permission.** Production agent purchases require a separate owner grant for the exact monthly upgrade, completed payment setup, a current quote, and an explicit `billing upgrade` command. Exhausted quota, login, and synthesis retries never purchase a plan automatically. Follow the skill's billing procedure before any purchase or synthesis retry.

## Permanently delete an account

Available from **v0.16.0**. Deletion cancels subscriptions and permanently removes
cloud files, credentials, and identity. Local downloads remain. `auth logout`
only signs out a CLI session; `auth delete` is not a command.

```bash
ttsbuddy account delete start --email you@example.com --json
# Securely supply the fresh six-digit email code through stdin, followed by EOF.
ttsbuddy account delete confirm --challenge-id <id> \
  --confirm-email you@example.com --code-stdin --yes --json
ttsbuddy account delete status --challenge-id <id> --json
# Before submission only:
ttsbuddy account delete cancel --challenge-id <id> --json
```

Fresh email verification, the exact prepared email, and `--yes` are required;
saved speech or agent credentials do not authorize deletion. Keep codes out of
arguments and logs. Use the same private config directory throughout.

Preparation reports `deleted: false`; only verified completion reports
`deleted: true`. Pending or unknown outcomes are not success. Preserve the
challenge and request IDs; use `status` before any explicit retry. For read-only
email verification, use `status --code-stdin`: it sends GET only.
See the [skill's deletion contract](skills/tts-buddy/SKILL.md#permanently-delete-an-account-v0160)
for recovery, receipt handling, and cancellation limits.

## Help and development

- [CLI guide](https://www.ttsbuddy.com/docs/developers/cli) and [API reference](https://www.ttsbuddy.com/docs/developers/api-reference)
- [Agent skill and technical reference](skills/tts-buddy/SKILL.md)
- [Report a bug](https://github.com/ngelik/ttsbuddy-cli/issues) or email [support@ttsbuddy.com](mailto:support@ttsbuddy.com)
- [Contributing](CONTRIBUTING.md) · [Security reporting](SECURITY.md)

Build and check the source:

```bash
make build
make test
make lint
make vuln
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup and contribution guidance, and [AGENTS.md](AGENTS.md) for release checks.

## License

[MIT](LICENSE)
