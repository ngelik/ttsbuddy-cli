#!/usr/bin/env bash
# Integration tests against a live (or local) TTSBuddy API.
# Requires: TTSBUDDY_API_KEY set, ttsbuddy binary built.
#
# Usage:
#   TTSBUDDY_API_KEY=ttsb_... ./scripts/integration_test.sh
#   TTSBUDDY_API_URL=http://localhost:54321/functions/v1/agent-tts ./scripts/integration_test.sh

set -euo pipefail

BINARY="${BINARY:-bin/ttsbuddy}"
PASS=0
FAIL=0
TOTAL=0

pass() { PASS=$((PASS+1)); TOTAL=$((TOTAL+1)); echo "  ✅ $1"; }
fail() { FAIL=$((FAIL+1)); TOTAL=$((TOTAL+1)); echo "  ❌ $1: $2"; }

run_test() {
    local name="$1"
    shift
    TOTAL=$((TOTAL+1))
    if eval "$@" >/dev/null 2>&1; then
        PASS=$((PASS+1))
        echo "  ✅ $name"
    else
        local code=$?
        FAIL=$((FAIL+1))
        echo "  ❌ $name (exit $code)"
    fi
}

run_test_expect_fail() {
    local name="$1"
    local expected_exit="$2"
    shift 2
    TOTAL=$((TOTAL+1))
    set +e
    eval "$@" >/dev/null 2>&1
    local actual_exit=$?
    set -e
    if [ "$actual_exit" = "$expected_exit" ]; then
        PASS=$((PASS+1))
        echo "  ✅ $name (exit $actual_exit as expected)"
    else
        FAIL=$((FAIL+1))
        echo "  ❌ $name (expected exit $expected_exit, got $actual_exit)"
    fi
}

# --- Prereqs ---
if [ -z "${TTSBUDDY_API_KEY:-}" ]; then
    echo "❌ TTSBUDDY_API_KEY not set. Export it first."
    exit 2
fi

if [ ! -x "$BINARY" ]; then
    echo "❌ Binary not found at $BINARY. Run 'make build' first."
    exit 2
fi

export TTSBUDDY_API_KEY
export TTSBUDDY_API_URL="${TTSBUDDY_API_URL:-}"

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

echo ""
echo "=== TTSBuddy CLI Integration Tests ==="
echo "Binary: $BINARY"
echo "API URL: ${TTSBUDDY_API_URL:-default}"
echo ""

# --- Version ---
echo "📋 Version"
run_test "version" "$BINARY version"
run_test "version --json" "$BINARY version --json | jq -e .version"

# --- Config ---
echo ""
echo "📋 Config"
run_test "config show" "$BINARY config"
run_test "config set voice" "$BINARY config set voice bf_emma"
run_test "config get voice" "test \"\$($BINARY config get voice)\" = bf_emma"
run_test_expect_fail "config set bad key" 2 "$BINARY config set key badformat"
run_test_expect_fail "config set unknown key" 2 "$BINARY config set bogus value"
run_test_expect_fail "config set speed out of range" 1 "$BINARY config set speed 3.0"
# Reset voice
$BINARY config set voice af_heart >/dev/null 2>&1

# --- Voices ---
echo ""
echo "📋 Voices"
run_test "voices (curated)" "$BINARY voices | grep af_heart"
run_test "voices --json" "$BINARY voices --json | jq -e '.[0].id'"

# --- Input validation ---
echo ""
echo "📋 Input validation"
run_test_expect_fail "no input" 2 "$BINARY speak"
run_test_expect_fail "--json and -o - conflict" 2 "$BINARY speak 'test' --json -o -"

# --- Speak (requires live TTS API) ---
echo ""
echo "📋 Speak (live API)"
run_test "speak inline" "$BINARY speak 'Hello from integration test' -o $TMPDIR/test1.mp3"
if [ -f "$TMPDIR/test1.mp3" ]; then
    pass "output file exists"
else
    fail "output file exists" "file not created"
fi

echo "hello from file" > "$TMPDIR/input.txt"
run_test "speak from file" "$BINARY speak -f $TMPDIR/input.txt -o $TMPDIR/test2.mp3"

run_test "speak --json" "$BINARY speak 'JSON test' --json | jq -e .status"
run_test "speak --no-download" "$BINARY speak 'URL only test' --no-download"

# --- Status ---
echo ""
echo "📋 Status"
run_test_expect_fail "status no job" 2 "HOME=$TMPDIR $BINARY status"

# If speak succeeded, test status with last job
if [ -f "$HOME/.ttsbuddy/last_job.json" ]; then
    JOB_ID=$(jq -r .job_id "$HOME/.ttsbuddy/last_job.json")
    run_test "status with job id" "$BINARY status $JOB_ID"
    run_test "status --json" "$BINARY status $JOB_ID --json | jq -e .status"
fi

run_test_expect_fail "status nonexistent" 1 "$BINARY status 00000000-0000-0000-0000-000000000000"

# --- Auth errors ---
echo ""
echo "📋 Auth errors"
run_test_expect_fail "invalid key" 1 "TTSBUDDY_API_KEY=ttsb_bad_key $BINARY speak 'test'"

# --- Summary ---
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📊 Results: $PASS/$TOTAL passed"
if [ "$FAIL" -gt 0 ]; then
    echo "❌ $FAIL test(s) failed"
    exit 1
else
    echo "✅ All tests passed"
fi
