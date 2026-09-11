#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

DEMO_API_URL="https://www.ttsbuddy.com/v1/cli-demo"
DEMO_API_KEY="ttsb_demo_cli"
CLI_KEY_ARGS=()

redact_api_key() {
  local key="${1:-}"
  if [[ -z "$key" ]]; then
    printf "(none)"
    return
  fi
  if [[ "$key" == ttsb_*_* ]]; then
    local rest="${key#ttsb_}"
    local public_id="${rest%%_*}"
    printf "ttsb_%s_..." "$public_id"
    return
  fi
  printf "***"
}

if [[ "${TTSBUDDY_DEMO_USE_REAL_KEY:-}" == "1" ]]; then
  : "${TTSBUDDY_API_KEY:?TTSBUDDY_API_KEY is required when TTSBUDDY_DEMO_USE_REAL_KEY=1}"
  export TTSBUDDY_API_URL="${TTSBUDDY_API_URL:-https://www.ttsbuddy.com/v1/agent-tts}"
else
  export TTSBUDDY_API_URL="$DEMO_API_URL"
  # The released CLI validates TTSBUDDY_API_KEY as a normal subscription
  # credential. Keep inherited credentials out of the constrained demo and
  # pass its intentionally short public credential through --key instead.
  unset TTSBUDDY_API_KEY
  CLI_KEY_ARGS=(--key "$DEMO_API_KEY")
fi

run_demo_cli() {
  if ((${#CLI_KEY_ARGS[@]} > 0)); then
    ttsbuddy "$@" "${CLI_KEY_ARGS[@]}"
  else
    ttsbuddy "$@"
  fi
}

if ! command -v ttsbuddy >/dev/null 2>&1; then
  echo "ttsbuddy was not found on PATH; building local CLI binary..."
  go build -o ./ttsbuddy ./cmd/ttsbuddy
  export PATH="$ROOT_DIR:$PATH"
fi

mkdir -p out

echo "== TTSBuddy CLI demo =="
echo "API URL: $TTSBUDDY_API_URL"
if [[ "${TTSBUDDY_DEMO_USE_REAL_KEY:-}" == "1" ]]; then
  echo "API key: $(redact_api_key "$TTSBUDDY_API_KEY")"
else
  echo "API key: $(redact_api_key "$DEMO_API_KEY") (explicit demo flag)"
fi
echo

echo "1. Incident handoff Markdown -> MP3"
run_demo_cli speak -f demo/oncall-summary.md --voice af_heart --speed 1 -o out/oncall-summary.mp3
ls -lh out/oncall-summary.mp3
echo

echo "2. Release notes Markdown -> JSON response"
run_demo_cli speak -f demo/release-notes.md --voice af_heart --speed 1 --json
echo

echo "3. Public docs webpage -> audio URL"
run_demo_cli web https://www.ttsbuddy.com/docs/developers/cli --voice af_heart --speed 1 --no-download
echo

echo "4. Raw MP3 stdout -> file"
run_demo_cli speak -f demo/oncall-summary.md --voice af_heart --speed 1 -o - > out/oncall-summary-stdout.mp3
ls -lh out/oncall-summary-stdout.mp3
echo

cat <<'MSG'
Demo mode is intentionally constrained:
- It accepts only sample files and one allowlisted docs URL.
- It returns pregenerated MP3 files.
- It is for no-signup CLI evaluation.

For real API mode:
  TTSBUDDY_DEMO_USE_REAL_KEY=1 TTSBUDDY_API_KEY=ttsb_your_... ./demo/cli-demo.sh
MSG
