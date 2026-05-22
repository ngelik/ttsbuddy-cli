#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export TTSBUDDY_API_URL="${TTSBUDDY_API_URL:-https://www.ttsbuddy.com/v1/cli-demo}"
export TTSBUDDY_API_KEY="${TTSBUDDY_API_KEY:-ttsb_demo_cli}"

if ! command -v ttsbuddy >/dev/null 2>&1; then
  echo "ttsbuddy was not found on PATH; building local CLI binary..."
  go build -o ./ttsbuddy .
  export PATH="$ROOT_DIR:$PATH"
fi

mkdir -p out

echo "== TTSBuddy CLI demo =="
echo "API URL: $TTSBUDDY_API_URL"
echo "API key: $TTSBUDDY_API_KEY"
echo

echo "1. Incident handoff Markdown -> MP3"
ttsbuddy speak -f demo/oncall-summary.md -o out/oncall-summary.mp3
ls -lh out/oncall-summary.mp3
echo

echo "2. Release notes Markdown -> JSON response"
ttsbuddy speak -f demo/release-notes.md --json
echo

echo "3. Public docs webpage -> audio URL"
ttsbuddy web https://www.ttsbuddy.com/docs/developers/cli --no-download
echo

echo "4. Raw MP3 stdout -> file"
ttsbuddy speak -f demo/oncall-summary.md -o - > out/oncall-summary-stdout.mp3
ls -lh out/oncall-summary-stdout.mp3
echo

cat <<'MSG'
Demo mode is intentionally constrained:
- It accepts only sample files and one allowlisted docs URL.
- It returns pregenerated MP3 files.
- It is for no-signup CLI evaluation.

For real API mode:
  unset TTSBUDDY_API_URL
  ttsbuddy config set key ttsb_your_key_here
MSG
