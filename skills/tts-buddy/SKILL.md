---
name: tts-buddy
description: Generate and download speech with TTS Buddy from text, Markdown, or public webpages. Use for TTSBuddy CLI workflows, voice selection, authorized authentication, asynchronous jobs, and recovery without duplicate synthesis. Also guides selection of the REST API or hosted MCP integration.
---

# TTS Buddy

Turn the user’s content into usable MP3 audio. Prefer the CLI for local files,
stdin, and shell automation. Preserve the user’s language, content, output
location, and authentication choices.

For ordinary audio tasks, the goal is a downloaded artifact—not merely an
accepted request, job ID, or temporary audio URL.

## Start here: ordinary text or webpage audio

### Execution context declaration

For synthesis submitted by a human, agent, or automation, pass the declaration
on the `speak` or `web` command with `--execution-context human|agent|automation`.
The equivalent environment variable is `TTSBUDDY_EXECUTION_CONTEXT`; a command
flag takes precedence. Omit it (or use `unknown`) when the caller cannot make a
reliable declaration. This value is analytics attribution only and does not
grant access or change billing.

The declaration is validated before submission. Invalid values are rejected;
status and download commands never rewrite the context recorded on the original
job, and the value does not affect idempotency keys.

Complete the workflow from this skill and live CLI discovery. Routine installation,
network diagnosis, and argument handling should not need coordinator advice.
A platform approval, unavailable mailbox, CAPTCHA, MFA, legal acceptance, or a
purchase can still require the owner; this skill does not remove those boundaries.

Follow this short sequence; use the numbered reference sections below only as needed:

1. **Always use the latest stable published CLI.** At the start of every workflow,
   query the official GitHub latest-release metadata below; do not rely on a
   remembered version or cached package-manager metadata. Check
   `command -v ttsbuddy` and the installed version against that release tag.
   Reuse it if current; otherwise prefer installing or upgrading through
   Homebrew. Only if Homebrew cannot supply the latest stable release, use the
   official release ZIP/archive in a private directory as described below.
   Set `TTSBUDDY_BIN` to the selected absolute executable path and
   verify `--version` matches the selected release (ignoring a leading `v`).
   Run all subsequent commands through that exact path, including help. Pin it
   for the workflow; check for the latest release again on the next workflow.
2. **Save one execution context.** Create a private absolute config directory
   and an output directory. Record their paths and the executable path in a
   local nonsecret run note. Shell exports do not necessarily persist between
   tool calls: set `TTSBUDDY_BIN` and `TTSBUDDY_CONFIG_DIR` again in each process,
   or pass them in the subprocess environment. Remove only stale demo/endpoint
   overrides for this run. Preserve an intentionally supplied real credential.
3. **Run doctor and check connectivity.** Offline signed-out doctor exit 1 is
   expected. If public HTTPS requests fail to resolve hosts in a restricted
   shell, retry the same narrow request through the execution tool's permitted
   network/escalation mechanism, if available. Allow normal approval review;
   do not change DNS, OS settings, endpoint trust, or credentials. Generic
   `AUTH_ERROR` / `AUTH_FAILED` can hide a transport failure: verify public
   connectivity before changing signup to login or restarting authentication.
4. **Authenticate once.** Use an authorized existing credential, otherwise email
   start/verify. For an account known to exist use sign-in; use `--signup` only
   when creating an account is requested. If signup explicitly reports an
   existing account, continue with sign-in for the same address. A generic
   failure alone does not establish account existence. Discover authorized
   mailbox tools or a signed-in browser; read only the relevant verification
   message for the requested address. Do not assume clipboard access works
   across browser and shell. Use the stdin recipe below for code entry.
5. **Discover one voice.** Run the filtered catalog command in section 4. Avoid
   dumping the full live catalog unless the filtered result is insufficient.
6. **Save original inputs and one UUID per request.** Python `uuid.uuid4()` or
   `uuidgen` can create the UUID once. Save it with input, voice/language/speed,
   and output path before submission. Use separate keys for text and webpage.
7. **Generate with an explicit output path and JSON.** Use the section 6 commands.
   Retain the exit code and JSON. On failure, follow section 7 rather than
   blindly resubmitting. With a known job, use `download` to recover it.
8. **Verify and finish.** Check the file is nonempty and matches `download.bytes`.
   Logout an isolated session created by this run and confirm signed-out state.
   Report saved files, remaining limitations, and any owner intervention honestly.

### Discover and install the latest stable release

Query `https://api.github.com/repos/ngelik/ttsbuddy-cli/releases/latest` for the
latest published stable release, excluding drafts and prereleases. Reuse an
existing executable only when its version matches that release.

**Prefer Homebrew whenever available and supported on the host.** Check
`command -v brew`. If installation or upgrade is needed, refresh its metadata
with `brew update`, then run the applicable command:

```sh
# Not installed through Homebrew:
brew install ngelik/tap/ttsbuddy

# Already installed through Homebrew:
brew upgrade ngelik/tap/ttsbuddy
```

Upgrade only TTS Buddy, not all installed packages. Resolve the installed
executable's absolute path and verify its `--version` matches the live GitHub
release tag. Cached `brew info` alone does not establish that the tap is stale.
Do not install Homebrew itself solely for this workflow.

**Use the official release ZIP/archive only as a fallback:** Homebrew is absent,
unsupported, installation/upgrade fails after addressing execution-environment
issues, or refreshed Homebrew metadata still cannot provide the latest stable
release. Record why the fallback was necessary.

Select the latest release asset matching the host OS and architecture. Use the
returned `browser_download_url` values for the ZIP/archive and checksum asset;
do not guess tags or asset names. Download via HTTPS, verify its SHA-256 against
its exact filename entry in the published checksum file, inspect archive
entries, and extract into a new private directory. Record the tag, checksum,
absolute binary path, and `--version`. A checksum establishes consistency with
the published release, not independent publisher identity.

If public requests fail in the sandbox, apply step 3 to these public reads and
later authenticated CLI commands as needed. Never substitute a custom service
host or an older binary to get around connectivity restrictions. If the latest
release cannot be verified or neither installation method supports this host,
report the blocker instead of silently falling back to an older version.

### Shell and subprocess execution

Examples below assume the selected absolute `TTSBUDDY_BIN` and private
`TTSBUDDY_CONFIG_DIR` are set in **that invocation**. Prefer an argument array:

```python
import os, subprocess
run_env = os.environ.copy()
run_env["TTSBUDDY_CONFIG_DIR"] = config_dir  # retained absolute run path
result = subprocess.run(
    [cli_path, "voices", "--language", "en", "--engine", "supertonic",
     "--recommended", "--json"],
    env=run_env, capture_output=True, text=True,
)
# Inspect result.returncode and parse result.stdout separately.
```

Do not turn a multiword command into one argument, join returned action argv
into shell text, or rely on zsh word splitting. Use `tts_exit` rather than the
read-only zsh variable `status`. For JSON receipts, keep exit status separately;
do not append shell status text to the JSON stream. Redact temporary audio URLs
when retaining or printing receipts for review.

## 1. Discover the service and choose an interface

Authoritative entry points:

- Agent quickstart: https://www.ttsbuddy.com/docs/developers/agent-quickstart
- CLI reference: https://www.ttsbuddy.com/docs/developers/cli
- Authentication: https://www.ttsbuddy.com/auth.md
- REST reference: https://www.ttsbuddy.com/docs/developers/api-reference
- API keys: https://www.ttsbuddy.com/docs/developers/api-keys
- Hosted MCP server card: https://www.ttsbuddy.com/.well-known/mcp/server-card.json
- Published CLI: https://github.com/ngelik/ttsbuddy-cli/releases

Documentation supports `Accept: text/markdown` where available.

Choose one interface:

| Interface | Use it for |
|---|---|
| CLI | Local text/Markdown files, stdin, public webpages, and downloaded MP3s |
| REST API | Application integration and direct HTTP job control |
| Hosted MCP | An MCP client with remote streamable-HTTP support |
| Website | Browser preview, account management, or required human authentication |

Hosted MCP is available at `https://mcp.ttsbuddy.com/mcp`. Inspect its current
server card and discover its live tools before using it. Do not build a wrapper
merely to access functionality already exposed by the hosted server.

The CLI, hosted MCP, and browser-local WebMCP are different interfaces.
The CLI itself is not an MCP server.

### Demo is not real synthesis

The CLI demo returns pregenerated audio for fixed sample inputs. A successful
demo proves that the demo path works; it does not prove arbitrary-text
synthesis or account authentication.

The website preview is a separate browser workflow.

For a user asking to narrate their own content, use real authenticated mode.
If intentionally testing the CLI demo, follow the current demo guide.
Its short public credential, `ttsb_demo_cli`, belongs in the explicit `--key`
flag with the demo endpoint, not in `TTSBUDDY_API_KEY`.

## 2. Prepare the CLI and execution context

Always use the latest stable published `ttsbuddy-cli` release, discovered and
verified as described above. A fixed minimum version is not sufficient.

Check an existing installation before installing:

```sh
"$TTSBUDDY_BIN" --version
"$TTSBUDDY_BIN" --help
```

Follow the Homebrew-first installation procedure above. Use an official release
ZIP/archive only when Homebrew cannot provide the latest stable version on this
host. Official archives cover macOS/Linux, amd64/arm64; select an available asset
from release metadata. Windows is outside the verified release matrix described
by this skill.

### Isolate temporary agent sessions

For an independent agent run, use a private absolute config directory:

```sh
TTS_RUN_DIR="$(mktemp -d)"
chmod 700 "$TTS_RUN_DIR"
export TTSBUDDY_CONFIG_DIR="$TTS_RUN_DIR"
```

The exact directory assigned to `TTSBUDDY_CONFIG_DIR` must be mode `0700`.
If you create a nested `config` directory, apply `chmod 700` to that child too;
a private parent alone does not satisfy this requirement. For
`CONFIG_DIR_PERMISSIONS`, inspect the returned path/mode and apply the suggested
`chmod` only to your own run directory, then rerun doctor. Do not cancel a
challenge or delete a lock to fix directory permissions.

Keep this directory consistent across authentication, synthesis, status,
download, and logout. It isolates saved configuration, pending authentication,
session credentials, last-job state, and locks.

Use the user’s existing configuration instead if they specifically requested
it. Do not overwrite or delete another workflow’s configuration.

An isolated directory does not cancel environment overrides. Check whether
`TTSBUDDY_API_URL`, `TTSBUDDY_CLI_AUTH_URL`, or a demo credential was inherited.
Remove stale demo overrides from this run before real synthesis. Preserve a
real credential intentionally supplied by the user.

Run:

```sh
"$TTSBUDDY_BIN" doctor --json
```

A signed-out doctor report can legitimately exit nonzero. Read its findings
instead of treating every nonzero result as an installation failure.

After authentication:

```sh
"$TTSBUDDY_BIN" doctor --online --json
```

Doctor is read-only. It does not send verification emails or generate audio.

Do not enable a custom API host just to silence a connection error. A custom
credentialed endpoint must be explicitly trusted with the bearer credential.

## 3. Authenticate using the available authorized method

Prefer an existing authorized credential over creating another account or
session.

### Existing API key

Use the orchestrator’s secret mechanism to supply `TTSBUDDY_API_KEY`.
Do not place a real key in example commands, shared transcripts, source files,
or logs.

API keys are created through the account dashboard. Do not invent a key,
registration endpoint, or unattended signup bypass.

### Email authentication

Before starting, establish whether the agent has authorized access to the
mailbox or must hand off code entry to the owner. Reuse the same retained
executable/config paths for start and verify; do not switch configurations.

Existing account:

```sh
"$TTSBUDDY_BIN" auth email start --email "$EMAIL" --json
```

New account, only when account creation is requested:

```sh
"$TTSBUDDY_BIN" auth email start --email "$EMAIL" --signup --json
```

Read the returned challenge ID, expiry, and next action. A prepared challenge
is not a successful login.

Continue in the same config directory:

```sh
"$TTSBUDDY_BIN" auth email verify \
  --challenge-id "$CHALLENGE_ID" \
  --code-stdin \
  --json
```

Supply the verification code through protected stdin. Do not put it in shell
commands, process arguments, source files, screenshots, or reports. Prefer the
execution tool's protected-input mechanism. Do not echo or log the code.

For a runner that supports a PTY and private input, this Python recipe prompts
without terminal echo, then closes the CLI's stdin automatically. Set the three
nonsecret environment variables before launching it with a PTY:

```sh
python3 -c '
import getpass, os, subprocess
cli_path = os.environ["TTSBUDDY_BIN"]
run_env = os.environ.copy()
assert os.path.isabs(cli_path)
assert os.path.isabs(run_env["TTSBUDDY_CONFIG_DIR"])
code = getpass.getpass("Email verification code: ")
result = subprocess.run(
    [cli_path, "auth", "email", "verify", "--challenge-id",
     os.environ["CHALLENGE_ID"], "--code-stdin", "--json"],
    input=code + "\n", text=True, capture_output=True, env=run_env,
)
print(result.stdout, end="")
raise SystemExit(result.returncode)
'
```

Enter the code through protected terminal input; do not paste it into the Python
source. If the runner has an in-memory secret input instead, pass that value to
`subprocess.run(input=...)` with the same argv/environment; no PTY is needed.
If no protected input/mailbox mechanism exists, preserve the challenge and hand
off to the owner. Ask for the verification code through a protected input channel
and wait; do not search unrelated accounts or report the entire audio task as
finished. Resume verification after the code arrives using the same challenge
and config directory, unless the returned expiry error requires a new start.
Do not claim fully unattended completion in that case.

When `auth email start --json` returns
`next_step.status: "awaiting_verification_code"`, follow its structured action.
`next_step.action.argv` preserves the challenge and explicit config override;
`required_inputs` identifies the missing verification code, supplied through
stdin. If the authorized mailbox is unavailable, use the
`if_mailbox_unavailable` handoff to ask the owner and wait. `expires_at` with
`expiry_scope: "cli_continuation_deadline"` describes the local continuation,
not a guarantee about the provider code's lifetime. These fields are additive;
older CLI responses still expose the top-level challenge, deadline and prose
next action.

Direct CLI `--code-stdin` reads until EOF. If using it directly in a PTY, a newline
alone may leave it waiting: finish input with EOF (usually Ctrl-D on an empty
line), or use the recipe above. An `ENV=value reader | cli` pipeline applies ENV
only to the reader, not the CLI. Avoid clipboard pipelines; `NO_PENDING_CHALLENGE`
should first trigger a config-path check, not another email start.

Important behavior:

- Pending authentication has a local continuation deadline; the provider’s
  email code can expire sooner.
- Repeating `start` while a challenge is pending does not necessarily send
  another email.
- A wrong code may permit another verification attempt on the same challenge.
- An expired challenge requires following the returned restart guidance.
- Successful login replaces the previous CLI session, including one on another
  machine. Coordinate this if the account already has an active workflow.
- A generic `422` or provider rejection does not prove that an address is
  disposable.
- Do not cycle through disposable addresses to evade provider restrictions.

To abandon an attempt:

```sh
"$TTSBUDDY_BIN" auth email cancel --challenge-id "$CHALLENGE_ID" --json
```

### Human handoff

When `BROWSER_AUTH_REQUIRED` or `human_action_required: true` is returned,
stop repeating email verification. Follow the browser action or hand off the
required step:

```sh
"$TTSBUDDY_BIN" auth browser
```

Mailbox access, CAPTCHA, MFA, and legal acceptance may require human
participation. Report exactly what is needed and preserve resumable state.
This skill does not authorize bypassing those requirements.

A browser-control failure is not automatically an authentication failure.
Distinguish a tool-reported lock, timeout, unavailable tab, and actual login
rejection. Do not change operating-system security settings as part of a TTS
task.

## 4. Discover a valid voice

Query the catalog instead of guessing IDs or speed ranges:

```sh
"$TTSBUDDY_BIN" voices --language en --engine supertonic --recommended --json
```

Replace the language and optional engine filter with the user’s requirements.

Inspect the returned voice’s:

- `id`
- `language` and `language_code`, when present
- `engine`, when present
- `min_speed`
- `max_speed`
- `recommended_speed`

Choose a matching returned voice and a speed within its advertised range.
Missing metadata means unknown, not unsupported or unlimited. If filtering
returns no voices, broaden the filter or consult the catalog; do not invent
an ID.

For multilingual Supertonic voices, select the spoken language separately.
Set voice, language, and speed explicitly when reproducibility matters.
CLI, REST, and account-preference defaults may differ.

## 5. Prepare narration input

Supported CLI patterns:

```sh
# Plain text
"$TTSBUDDY_BIN" speak "Text to narrate"

# Text or Markdown file
"$TTSBUDDY_BIN" speak --file article.md

# Text supplied through stdin
"$TTSBUDDY_BIN" speak -

# Public HTML webpage
"$TTSBUDDY_BIN" web https://example.com/public-article
```

Use file input or protected stdin for private or lengthy content rather than
placing the narration in process arguments.

Markdown files ending in `.md` or `.markdown` receive deterministic narration
cleanup. Formatting syntax, image references, link URLs, and fenced code
blocks are removed while readable text is retained.

Use `--raw` if literal Markdown should be spoken. Inline text and stdin do not
receive that automatic Markdown-file preprocessing.

Do not assume binary PDF or DOCX files are valid `--file` input. Extract text
with an appropriate document tool first and verify the reading order.

`web` handles public HTTP(S) HTML, not authenticated pages, private network
addresses, or arbitrary downloadable document formats. It extracts content
locally and submits narration content and webpage metadata. Translation
behavior depends on source and target language; set options deliberately.

The documented maximum is 500,000 characters per synthesis request. Split
larger input at meaningful boundaries, assign each chunk its own request
identity, and preserve playback order.

## 6. Generate and save real audio

Before submission, retain:

- The exact input or source file
- Voice, language, speed, and preprocessing choices
- A unique idempotency key for this logical request
- The intended output path

Generate the key once using the orchestrator’s UUID facility. Do not generate
a new key inside an automatic retry loop.

Create the output parent directory first.

```sh
"$TTSBUDDY_BIN" speak --file "$INPUT_FILE" \
  --voice "$VOICE_ID" \
  --language "$LANGUAGE" \
  --speed "$SPEED" \
  --idempotency-key "$REQUEST_KEY" \
  --output "$OUTPUT_FILE" \
  --json
```

For a public webpage:

```sh
"$TTSBUDDY_BIN" web "$PUBLIC_URL" \
  --voice "$VOICE_ID" \
  --language "$LANGUAGE" \
  --speed "$SPEED" \
  --idempotency-key "$REQUEST_KEY" \
  --output "$OUTPUT_FILE" \
  --json
```

Parse stdout as JSON and inspect the process exit status. Preserve structured
error output even when the process exits nonzero.

### Output semantics

| Command/mode | Result |
|---|---|
| `speak --output file.mp3 --json` | Saves audio and returns JSON with download details |
| `web --output file.mp3 --json` | Saves audio and returns JSON with download details |
| Bare `speak --json` or `web --json` | Returns metadata without saving audio |
| `status JOB_ID --json` | Inspects the job; does not download |
| `download JOB_ID --output file.mp3 --json` | Waits if needed, then saves existing job audio |
| `--output -` | Binary stdout workflow; keep separate from JSON |

After a successful saved result, `download.path` is the absolute destination
and `download.bytes` is the actual saved byte count.

Do not interpret a successful submission or a temporary URL as proof that a
local file exists.

## 7. Recover without duplicate synthesis

Once a job ID is known, prefer job recovery:

```sh
"$TTSBUDDY_BIN" status "$JOB_ID" --json

"$TTSBUDDY_BIN" download "$JOB_ID" \
  --output "$OUTPUT_FILE" \
  --json
```

`download` polls when necessary and never submits another synthesis request.
Use it after interrupted polling or file transfer.

For automatic recovery, read these fields when present:

- `error.code`
- `error.reason`
- `error.retryable`
- `error.retry_after_seconds`
- `error.next_action`
- `error.idempotency_key`
- `error.action`

`error.action` can contain `type`, `argv`, `required_inputs`, and `url`.
Common action types include `authenticate`, `browser_authentication`,
`verify_code`, `retry_submission`, `status`, `download`, `account`, and
`input_correction`.

Execute a suitable `action.argv` as a process argument array, never by joining
it into a shell command or passing it to `eval`. Check that it matches the
current task and supply any required inputs through the appropriate channel.

An action describes the next step; it does not grant permission for purchases,
account changes, or other new external actions.

### Recovery decisions

| Condition | Next step |
|---|---|
| Job ID known; polling or transfer interrupted | Inspect or download that same job |
| Submission outcome ambiguous; no job ID | Retry identical input/options with the original idempotency key |
| Original stdin no longer available | Recover the input before retrying; do not invent replacement text |
| Session expired or revoked | Reauthenticate; then recover the known job |
| Invalid email code | Follow retry guidance for the same challenge |
| Browser/human verification required | Hand off; stop email retries |
| Rate limited | Wait for returned retry timing, then retry the appropriate operation |
| Quota exhausted or subscription inactive | Report the required account action; do not loop synthesis |
| Invalid input or excessive length | Correct input before submitting |
| Definitively failed or expired job | Follow fresh-submission guidance; a new key may be required |
| Unknown or unchanged failure | Stop after bounded attempts and report evidence |

Reuse an idempotency key only for the identical logical request. Changing text,
voice, language, speed, or webpage content changes the request.

For ambiguous webpage submissions, retain the original input/context: fetching
the same URL later may produce different content.

Respect returned retry timing. If a transient error provides no timing, use a
small bounded backoff, such as three attempts, within the task’s deadline.
Do not automatically retry authentication, quota, validation, or human-handoff
failures as if they were transient network errors.

Exit `130` indicates interruption. Preserve the structured recovery information
and any known job ID rather than starting over.

## 8. Interpret metadata honestly

Completion metadata distinguishes reported or estimated values from actual
local observations:

| Field | Meaning |
|---|---|
| `audio.duration_source = provider` | Provider-reported duration, not independent measurement |
| `audio.duration_source = estimated` | Estimated duration |
| `audio.duration_source = unknown` | Provenance is unavailable |
| `audio.file_size_source = content_length` | Size observed from successful HTTP headers |
| `audio.file_size_source = estimated` | Estimated size |
| `audio.file_size_source = unknown` | Provenance is unavailable |
| `download.bytes` | Actual bytes saved locally |
| Missing `expires_at` | Expiry is unknown |

New completion metadata is persisted so completion, status, and replay agree.
Do not relabel estimates as measured facts.

Download temporary audio promptly. A job’s existence does not guarantee its
audio remains available indefinitely.

If accurate duration or media validation is required, inspect the downloaded
file with an available media tool such as ffprobe/ffmpeg. That is an optional
verification step, not a runtime dependency for ordinary CLI use.

## 9. REST alternative

Use the REST API when direct HTTP integration is more appropriate.

```text
POST https://www.ttsbuddy.com/v1/agent-tts
GET  https://www.ttsbuddy.com/v1/agent-tts?id=JOB_ID
```

Submission headers:

```text
Authorization: Bearer <authorized credential>
Content-Type: application/json
Idempotency-Key: <stable key for this logical request>
```

Example request body:

```json
{
  "text": "Original text to narrate",
  "voice": "st_m1",
  "language": "en",
  "speed": 1
}
```

Select valid options from discovery before using the example.

A `200` completion can include `audio_url`; a `202` means processing continues.
Poll the returned job ID until terminal state. Respect polling/rate-limit
guidance and download on completion.

Use the documented query parameter `id`; do not invent a
`/agent-tts/JOB_ID` route.

For direct downloads, treat returned URLs as temporary data. Do not forward
the API bearer credential to a storage host or blindly follow redirects into
private network destinations. Prefer the CLI’s existing downloader when it
fits the task.

## 10. Finish and report

For a requested local MP3:

1. Confirm completion.
2. Confirm the destination exists and is nonempty.
3. Confirm the saved size matches `download.bytes` when returned.
4. Provide the local artifact or requested output location.
5. Retain the job ID when useful for subsequent recovery.

For an isolated temporary CLI session created for this task:

```sh
"$TTSBUDDY_BIN" auth logout --json
```

Verify logout before deleting temporary authentication files. Removing local
configuration alone does not revoke the remote session. Do not log out or
revoke credentials belonging to another active workflow.

Keep the final response brief:

- What was generated
- Where the audio was saved
- Voice/language if useful
- Whether duration is estimated or measured, if mentioned
- Any unresolved limitation or required human step

Do not claim successful authentication from challenge creation, real synthesis
from a canned demo, a successful download from metadata alone, or completion
of a test that was blocked by unavailable browser or mailbox access.
