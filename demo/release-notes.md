# Release notes: CLI 0.4.2

## Added

- New `ttsbuddy web <url>` command for turning readable webpage text into speech.
- `--no-download` mode for scripts that only need the generated MP3 URL.
- JSON output for automation with `--json`.

## Changed

- Markdown files are cleaned before speech generation so headings and links sound natural.
- File inputs use deterministic idempotency keys, which makes retrying safer.

## Fixed

- Raw MP3 stdout now refuses unsafe download hosts.
- Long input errors mention the 500,000-character limit.
