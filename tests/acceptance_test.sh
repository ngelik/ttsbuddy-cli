#!/usr/bin/env bash
# Acceptance tests for ttsbuddy CLI.
# Runs against the live API, or the explicitly gated local prepaid acceptance
# path, using the installed or dev-built binary.
#
# Usage:
#   AUTH_ONLY=1 BINARY=bin/ttsbuddy ./tests/acceptance_test.sh
#   PREPAID_ACCEPTANCE=1 PREPAID_PAYMENT_OPT_IN=BASE_SEPOLIA_TEST_USDC_ONLY \
#     PREPAID_LOCAL_STACK_ACK=CLEAN_LOCAL_SUPABASE_ONLY PREPAID_WALLET=local \
#     PREPAID_MAX_PRICE=5.00 PREPAID_EXPECTED_PAYEE_ADDRESS=0x... \
#     PREPAID_EXPECTED_CLI_REVISION=<40-hex> PREPAID_RECEIPT_DIR=/tmp/... \
#     TTSBUDDY_API_URL=http://127.0.0.1:54321/functions/v1/agent-tts \
#     TTSBUDDY_ALLOW_CUSTOM_API_URL=true BINARY=bin/ttsbuddy ./tests/acceptance_test.sh
#   TTSBUDDY_API_KEY=ttsb_... ./tests/acceptance_test.sh
#   BINARY=bin/ttsbuddy TTSBUDDY_API_KEY=ttsb_... ./tests/acceptance_test.sh
#
# Requires: jq, ttsbuddy binary on $PATH or BINARY set

set -uo pipefail

BINARY="${BINARY:-ttsbuddy}"
AUTH_ONLY="${AUTH_ONLY:-0}"
PREPAID_ACCEPTANCE="${PREPAID_ACCEPTANCE:-0}"
POST_DELAY="${POST_DELAY:-65}"
PASS=0
FAIL=0
SKIP=0
TOTAL=0
RATE_LIMITED_COUNT=0

# --- Setup ---

if ! command -v "$BINARY" &>/dev/null && [ ! -x "$BINARY" ]; then
    echo "❌ Binary not found: $BINARY"
    exit 2
fi

if ! command -v jq &>/dev/null; then
    echo "❌ jq not found. Install it first."
    exit 2
fi

TB_HOME="$(mktemp -d /tmp/ttsbuddy-accept.XXXXXX)"
TB_OUT="$(mktemp -d /tmp/ttsbuddy-out.XXXXXX)"
ACCEPTANCE_RUN_ID="$(date +%Y%m%d%H%M%S)-$$-$RANDOM"
trap 'rm -rf "$TB_HOME" "$TB_OUT"' EXIT

tb() { HOME="$TB_HOME" "$BINARY" "$@"; }

# Captured job_id for status tests
JOB_ID=""

echo ""
echo "=== TTSBuddy CLI Acceptance Tests ==="
echo "Binary:     $BINARY ($(command -v "$BINARY" 2>/dev/null || echo "$BINARY"))"
if [ -n "${TTSBUDDY_API_KEY:-}" ]; then
    echo "API Key:    set (redacted)"
else
    echo "API Key:    not set"
fi
echo "POST delay: ${POST_DELAY}s"
echo "Run ID:     $ACCEPTANCE_RUN_ID"
echo "TB_HOME:    $TB_HOME"
echo "TB_OUT:     $TB_OUT"
echo ""

# --- Helpers ---

pass() {
    PASS=$((PASS + 1))
    TOTAL=$((TOTAL + 1))
    echo "  ✅ $1"
}

fail() {
    FAIL=$((FAIL + 1))
    TOTAL=$((TOTAL + 1))
    echo "  ❌ $1: $2"
    # Dump stderr on failure for debugging
    if [ -f "$TB_OUT/_stderr" ]; then
        local lines
        lines=$(wc -l < "$TB_OUT/_stderr")
        if [ "$lines" -gt 0 ]; then
            echo "     stderr: $(head -3 "$TB_OUT/_stderr")"
        fi
    fi
}

skip() {
    SKIP=$((SKIP + 1))
    TOTAL=$((TOTAL + 1))
    echo "  ⏭️  $1: $2"
}

is_audio_file() {
    local path="$1"
    [ -s "$path" ] && file "$path" 2>/dev/null | grep -qi "audio\|mpeg\|mp3\|ID3"
}

print_summary() {
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "📊 Results: $PASS/$TOTAL passed"
    if [ "$SKIP" -gt 0 ]; then
        echo "⏭️  $SKIP skipped"
    fi
    if [ "$RATE_LIMITED_COUNT" -gt 0 ]; then
        echo "🔄 $RATE_LIMITED_COUNT rate-limit retries"
    fi
    if [ "$FAIL" -gt 0 ]; then
        echo "❌ $FAIL test(s) failed"
        return 1
    fi
    echo "✅ All tests passed"
}

# run_test <id> <expected_exit> <cmd...>
run_test() {
    local id="$1" expected="$2"
    shift 2
    set +e
    "$@" >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
    local actual=$?
    set -e
    if [ "$actual" -eq "$expected" ]; then
        pass "$id"
    else
        fail "$id" "expected exit $expected, got $actual"
    fi
}

# run_test_stdout <id> <expected_exit> <substr> <cmd...>
run_test_stdout() {
    local id="$1" expected="$2" substr="$3"
    shift 3
    set +e
    "$@" >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
    local actual=$?
    set -e
    if [ "$actual" -ne "$expected" ]; then
        fail "$id" "expected exit $expected, got $actual"
        return
    fi
    if grep -q "$substr" "$TB_OUT/_stdout"; then
        pass "$id"
    else
        fail "$id" "stdout missing '$substr'"
    fi
}

# run_test_stderr <id> <expected_exit> <substr> <cmd...>
run_test_stderr() {
    local id="$1" expected="$2" substr="$3"
    shift 3
    set +e
    "$@" >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
    local actual=$?
    set -e
    if [ "$actual" -ne "$expected" ]; then
        fail "$id" "expected exit $expected, got $actual"
        return
    fi
    if grep -q "$substr" "$TB_OUT/_stderr"; then
        pass "$id"
    else
        fail "$id" "stderr missing '$substr'"
    fi
}

# run_test_json <id> <cmd...> — checks exit 0 + valid JSON on stdout
run_test_json() {
    local id="$1"
    shift
    set +e
    "$@" >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
    local actual=$?
    set -e
    if [ "$actual" -ne 0 ]; then
        fail "$id" "expected exit 0, got $actual"
        return
    fi
    if jq -e . "$TB_OUT/_stdout" >/dev/null 2>&1; then
        pass "$id"
    else
        fail "$id" "stdout is not valid JSON"
    fi
}

# post_test <id> <expected_exit> <cmd...> — sleeps, runs, retries on rate limit
post_test() {
    local id="$1" expected="$2"
    shift 2

    echo "    ⏳ waiting ${POST_DELAY}s..."
    sleep "$POST_DELAY"

    set +e
    "$@" >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
    local actual=$?
    set -e

    # Retry once on rate limit
    if [ "$actual" -ne "$expected" ] && grep -q "rate limited" "$TB_OUT/_stderr" 2>/dev/null; then
        echo "    🔄 rate limited, retrying after ${POST_DELAY}s..."
        RATE_LIMITED_COUNT=$((RATE_LIMITED_COUNT + 1))
        if [ "$RATE_LIMITED_COUNT" -gt 2 ]; then
            fail "$id" "too many rate limits (>2) — pacing problem"
            return
        fi
        sleep "$POST_DELAY"
        set +e
        "$@" >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
        actual=$?
        set -e
    fi

    if [ "$actual" -eq "$expected" ]; then
        pass "$id"
    else
        fail "$id" "expected exit $expected, got $actual"
    fi
}

# --- Explicitly gated prepaid acceptance helpers ---

prepaid_die() {
    echo "❌ Prepaid acceptance stopped: $1"
    exit 2
}

prepaid_require_env() {
    local name="$1"
    if [ -z "${!name:-}" ]; then
        prepaid_die "$name is required"
    fi
}

prepaid_lower() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

# Capture transient command output under TB_OUT. On failure, do not echo the
# captured response: payment responses and URLs are not safe receipt material.
prepaid_capture() {
    local id="$1" stdout_path="$2" stderr_path="$3"
    shift 3
    set +e
    "$@" >"$stdout_path" 2>"$stderr_path"
    local actual=$?
    set -e
    if [ "$actual" -ne 0 ]; then
        fail "$id" "exit $actual (captured output withheld)"
        return 1
    fi
    pass "$id"
}

prepaid_write_tts_receipt() {
    local input="$1" output="$2"
    jq '{
        success, status, job_id,
        audio: (.audio // null | if . == null then null else {
            duration_seconds, file_size_bytes, format, voice, speed, expires_at
        } end),
        billing: (.billing // null | if . == null then null else {
            mode, units, request_units, allowance_units, reserved_units,
            consumed_units, remaining_units
        } end),
        stats: (.stats // null | if . == null then null else {
            characters_count, speech_length_seconds, file_size_bytes,
            generation_seconds, generation_chars_per_second
        } end),
        meta: (.meta // null | if . == null then null else {
            request_id, api_version
        } end)
    }' "$input" >"$output"
}

prepaid_finalize_receipts() {
    local receipt_dir="$1"
    local secret name

    # Reject credential-shaped values and forbidden header/secret names without
    # printing the matching content.
    if grep -ERq '\bttsp_[a-z0-9-]{8,128}_[a-zA-Z0-9_-]{32,128}\b|\bttsb_[^[:space:]]+|\bttsc_[^[:space:]]+' "$receipt_dir"; then
        prepaid_die "receipt credential redaction check failed; quarantine the receipt directory"
    fi
    if grep -ERq 'PAYMENT-SIGNATURE|private[_ -]?key|CDP_API_KEY_SECRET|CDP_WALLET_SECRET' "$receipt_dir"; then
        prepaid_die "receipt redaction check failed; quarantine the receipt directory"
    fi
    for name in TTSBUDDY_EVM_PRIVATE_KEY CDP_API_KEY_ID CDP_API_KEY_SECRET CDP_WALLET_SECRET; do
        secret="${!name:-}"
        if [ -n "$secret" ] && grep -FRq -- "$secret" "$receipt_dir"; then
            prepaid_die "receipt contains a configured secret; quarantine the receipt directory"
        fi
    done

    (
        cd "$receipt_dir" || exit 1
        for file in *.json; do
            shasum -a 256 "$file"
        done
    ) >"$receipt_dir/SHA256SUMS"
    chmod 400 "$receipt_dir"/*.json "$receipt_dir/SHA256SUMS"
    chmod 500 "$receipt_dir"
}

run_prepaid_acceptance() {
    local expected_api_url="http://127.0.0.1:54321/functions/v1/agent-tts"
    local expected_asset="0x036cbd53842c5426634e7929541ec2318f3dcf7e"
    local metadata revision modified starter_count
    local expected_payee_lc
    local plans_raw buy_raw before_raw speak_raw poll_raw after_raw
    local job_id prepaid_text initial_consumed initial_remaining expected_consumed expected_remaining

    [ "$AUTH_ONLY" = "0" ] || prepaid_die "AUTH_ONLY and PREPAID_ACCEPTANCE are mutually exclusive"
    [ "$PREPAID_ACCEPTANCE" = "1" ] || prepaid_die "PREPAID_ACCEPTANCE must be 0 or 1"
    [ "${PREPAID_PAYMENT_OPT_IN:-}" = "BASE_SEPOLIA_TEST_USDC_ONLY" ] || \
        prepaid_die "set PREPAID_PAYMENT_OPT_IN=BASE_SEPOLIA_TEST_USDC_ONLY to authorize the testnet payment"
    [ "${PREPAID_LOCAL_STACK_ACK:-}" = "CLEAN_LOCAL_SUPABASE_ONLY" ] || \
        prepaid_die "set PREPAID_LOCAL_STACK_ACK=CLEAN_LOCAL_SUPABASE_ONLY after verifying the clean local stack"
    [ "${TTSBUDDY_API_URL:-}" = "$expected_api_url" ] || \
        prepaid_die "TTSBUDDY_API_URL must be exactly $expected_api_url"
    [ "${TTSBUDDY_ALLOW_CUSTOM_API_URL:-}" = "true" ] || \
        prepaid_die "TTSBUDDY_ALLOW_CUSTOM_API_URL must be exactly true"
    [ -z "${TTSBUDDY_API_KEY:-}" ] || prepaid_die "TTSBUDDY_API_KEY must be unset"
    [ -z "${TTSBUDDY_ACCESS_PASS:-}" ] || prepaid_die "TTSBUDDY_ACCESS_PASS must be unset so the purchased pass is used"

    prepaid_require_env PREPAID_WALLET
    prepaid_require_env PREPAID_MAX_PRICE
    prepaid_require_env PREPAID_EXPECTED_PAYEE_ADDRESS
    prepaid_require_env PREPAID_EXPECTED_CLI_REVISION
    prepaid_require_env PREPAID_RECEIPT_DIR
    [ "$PREPAID_MAX_PRICE" = "5.00" ] || prepaid_die "PREPAID_MAX_PRICE must be exactly 5.00 for the frozen starter plan"
    [[ "$PREPAID_EXPECTED_PAYEE_ADDRESS" =~ ^0x[0-9a-fA-F]{40}$ ]] || prepaid_die "PREPAID_EXPECTED_PAYEE_ADDRESS is invalid"
    expected_payee_lc="$(prepaid_lower "$PREPAID_EXPECTED_PAYEE_ADDRESS")"
    [ "$expected_payee_lc" != "0x0000000000000000000000000000000000000000" ] || prepaid_die "payee must be nonzero"
    [[ "$PREPAID_EXPECTED_CLI_REVISION" =~ ^[0-9a-f]{40}$ ]] || prepaid_die "PREPAID_EXPECTED_CLI_REVISION must be a full lowercase commit"

    case "$PREPAID_WALLET" in
        local)
            [ "${PREPAID_LOCAL_WALLET_OPT_IN:-}" = "1" ] || prepaid_die "set PREPAID_LOCAL_WALLET_OPT_IN=1"
            prepaid_require_env TTSBUDDY_EVM_PRIVATE_KEY
            ;;
        cdp)
            [ "${PREPAID_CDP_WALLET_OPT_IN:-}" = "1" ] || prepaid_die "set PREPAID_CDP_WALLET_OPT_IN=1"
            prepaid_require_env CDP_API_KEY_ID
            prepaid_require_env CDP_API_KEY_SECRET
            prepaid_require_env CDP_WALLET_SECRET
            prepaid_require_env TTSBUDDY_CDP_EVM_ACCOUNT_ADDRESS
            ;;
        *) prepaid_die "PREPAID_WALLET must be local or cdp" ;;
    esac

    command -v go >/dev/null 2>&1 || prepaid_die "go is required to inspect binary build metadata"
    command -v shasum >/dev/null 2>&1 || prepaid_die "shasum is required for the receipt manifest"
    [ -d "$PREPAID_RECEIPT_DIR" ] || prepaid_die "PREPAID_RECEIPT_DIR must be an existing mktemp directory"
    [ ! -L "$PREPAID_RECEIPT_DIR" ] || prepaid_die "PREPAID_RECEIPT_DIR must not be a symlink"
    [ -z "$(find "$PREPAID_RECEIPT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ] || prepaid_die "PREPAID_RECEIPT_DIR must be empty"
    chmod 700 "$PREPAID_RECEIPT_DIR"

    metadata=$(go version -m "$BINARY" 2>/dev/null) || prepaid_die "cannot read Go build metadata from $BINARY"
    revision=$(printf '%s\n' "$metadata" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' | tail -1)
    modified=$(printf '%s\n' "$metadata" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.modified=//p' | tail -1)
    [ "$revision" = "$PREPAID_EXPECTED_CLI_REVISION" ] || prepaid_die "binary VCS revision does not match PREPAID_EXPECTED_CLI_REVISION"
    [ "$modified" = "false" ] || prepaid_die "binary must report vcs.modified=false"

    plans_raw="$TB_OUT/_prepaid_plans.json"
    buy_raw="$TB_OUT/_prepaid_buy.json"
    before_raw="$TB_OUT/_prepaid_before.json"
    speak_raw="$TB_OUT/_prepaid_speak.json"
    poll_raw="$TB_OUT/_prepaid_poll.json"
    after_raw="$TB_OUT/_prepaid_after.json"

    jq -n \
        --arg run_id "$ACCEPTANCE_RUN_ID" \
        --arg revision "$revision" \
        --arg binary "$BINARY" \
        --arg wallet "$PREPAID_WALLET" \
        --arg api_url "$expected_api_url" \
        '{schema_version: 1, run_id: $run_id, cli_revision: $revision,
          binary: $binary, wallet_backend: $wallet, api_url: $api_url,
          network: "eip155:84532", asset: "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
          payment_environment: "Base Sepolia test USDC", local_home: true}' \
        >"$PREPAID_RECEIPT_DIR/00-run.json"

    echo "📋 PREPAID. Explicit Base Sepolia private-beta smoke"
    prepaid_capture "PREPAID.1 access plans" "$plans_raw" "$TB_OUT/_prepaid_plans.stderr" tb --json access plans || exit 1
    starter_count=$(jq '[.plans[] | select(.sku == "starter")] | length' "$plans_raw" 2>/dev/null) || prepaid_die "plans response is not valid JSON"
    [ "$starter_count" -eq 1 ] || prepaid_die "expected exactly one starter plan"
    jq -e --arg asset "$expected_asset" --arg payee "$expected_payee_lc" '
        .plans[] | select(.sku == "starter") |
        .version == 1 and .price.atomic == "5000000" and
        (.price.asset | ascii_downcase) == $asset and .price.asset_decimals == 6 and
        .price.network == "eip155:84532" and (.price.pay_to | ascii_downcase) == $payee and
        .allowance_units == 500000 and .request_limit_units == 100000 and
        .valid_for_seconds > 0
    ' "$plans_raw" >/dev/null || prepaid_die "starter plan does not match the frozen network, asset, payee, price, or limits"
    jq '{plans: [.plans[] | select(.sku == "starter") | {
        sku, version, price: {display, atomic, asset, asset_decimals, network, pay_to},
        allowance_units, request_limit_units, valid_for_seconds, voice_policy
    }]}' "$plans_raw" >"$PREPAID_RECEIPT_DIR/01-plans.json"

    prepaid_capture "PREPAID.2 buy starter" "$buy_raw" "$TB_OUT/_prepaid_buy.stderr" \
        tb --json access buy starter --wallet "$PREPAID_WALLET" --max-price "$PREPAID_MAX_PRICE" || exit 1
    jq -e '
        .success == true and .saved == true and .status == "active" and
        .allowance_units == 500000 and .reserved_units == 0 and
        .consumed_units == 0 and .remaining_units == 500000 and
        .request_limit_units == 100000 and (.pass | endswith("_...")) and
        .receipt.network == "eip155:84532" and .receipt.amount == "5000000" and
        (.receipt.purchase_id | type == "string" and length > 0) and
        (.receipt.transaction | test("^0x[0-9a-fA-F]{64}$"))
    ' "$buy_raw" >/dev/null || prepaid_die "purchase response failed the settled-pass invariants"
    jq '{success, saved, status, allowance_units, reserved_units, consumed_units,
        remaining_units, request_limit_units, expires_at,
        receipt: {purchase_id: .receipt.purchase_id, network: .receipt.network,
          transaction: .receipt.transaction, asset: .receipt.asset, amount: .receipt.amount}}' \
        "$buy_raw" >"$PREPAID_RECEIPT_DIR/02-buy.json"

    prepaid_capture "PREPAID.3 initial access status" "$before_raw" "$TB_OUT/_prepaid_before.stderr" tb --json access status || exit 1
    jq -e '.status == "active" and .allowance_units == 500000 and
        .reserved_units == 0 and .consumed_units == 0 and .remaining_units == 500000 and
        .request_limit_units == 100000' "$before_raw" >/dev/null || prepaid_die "initial pass status is inconsistent"
    initial_consumed=$(jq -r '.consumed_units' "$before_raw")
    initial_remaining=$(jq -r '.remaining_units' "$before_raw")
    jq '{status, allowance_units, reserved_units, consumed_units, remaining_units,
        request_limit_units, expires_at, plan: {sku: .plan.sku, version: .plan.version},
        receipt: {purchase_id: .receipt.purchase_id, network: .receipt.network,
          transaction: .receipt.transaction, asset: .receipt.asset, amount: .receipt.amount}}' \
        "$before_raw" >"$PREPAID_RECEIPT_DIR/03-status-before.json"

    prepaid_text="Prepaid access pass acceptance."
    prepaid_capture "PREPAID.4 direct speak, poll, and download" "$speak_raw" "$TB_OUT/_prepaid_speak.stderr" \
        tb --json speak "$prepaid_text" --idempotency-key "prepaid-${ACCEPTANCE_RUN_ID}-direct" \
        --output "$TB_OUT/prepaid-direct.mp3" --timeout 10m || exit 1
    job_id=$(jq -r '.job_id // empty' "$speak_raw")
    [ -n "$job_id" ] || prepaid_die "speak response did not include a job_id"
    [ -s "$TB_OUT/prepaid-direct.mp3" ] || prepaid_die "speak did not download a non-empty audio file"
    is_audio_file "$TB_OUT/prepaid-direct.mp3" || prepaid_die "downloaded file was not recognized as audio"
    prepaid_write_tts_receipt "$speak_raw" "$PREPAID_RECEIPT_DIR/04-speak.json"

    prepaid_capture "PREPAID.5 terminal job status" "$poll_raw" "$TB_OUT/_prepaid_poll.stderr" \
        tb --json status "$job_id" --watch --timeout 30s || exit 1
    jq -e --arg job_id "$job_id" '.job_id == $job_id and .status == "completed"' "$poll_raw" >/dev/null || \
        prepaid_die "job status did not confirm the same completed job"
    prepaid_write_tts_receipt "$poll_raw" "$PREPAID_RECEIPT_DIR/05-job-status.json"

    prepaid_capture "PREPAID.6 final access status" "$after_raw" "$TB_OUT/_prepaid_after.stderr" tb --json access status || exit 1
    expected_consumed=$((initial_consumed + ${#prepaid_text}))
    expected_remaining=$((initial_remaining - ${#prepaid_text}))
    jq -e --argjson consumed "$expected_consumed" --argjson remaining "$expected_remaining" '
        .status == "active" and .reserved_units == 0 and
        .consumed_units == $consumed and .remaining_units == $remaining
    ' "$after_raw" >/dev/null || prepaid_die "final pass counters do not match the direct-text UTF-16 reservation"
    jq '{status, allowance_units, reserved_units, consumed_units, remaining_units,
        request_limit_units, expires_at, plan: {sku: .plan.sku, version: .plan.version},
        receipt: {purchase_id: .receipt.purchase_id, network: .receipt.network,
          transaction: .receipt.transaction, asset: .receipt.asset, amount: .receipt.amount}}' \
        "$after_raw" >"$PREPAID_RECEIPT_DIR/06-status-after.json"

    run_test "PREPAID.7 access forget" 0 tb access forget
    run_test "PREPAID.8 status after forget" 1 tb access status

    prepaid_finalize_receipts "$PREPAID_RECEIPT_DIR"
    echo "  🔒 Redacted receipt set: $PREPAID_RECEIPT_DIR"
    echo ""
    print_summary
}

# ============================================================
# AUTH. Safe signed-out lifecycle (never calls a live service)
# ============================================================

if [ "$PREPAID_ACCEPTANCE" = "1" ]; then
    run_prepaid_acceptance
    exit $?
fi

if [ "$PREPAID_ACCEPTANCE" != "0" ]; then
    echo "❌ PREPAID_ACCEPTANCE must be 0 or 1."
    exit 2
fi

echo "📋 AUTH. Signed-out and local-only lifecycle"
run_test_stdout "AUTH.1 auth --help" 0 "login" tb auth --help
run_test_stderr "AUTH.2 signed-out status" 1 "Not signed in" tb auth status
run_test_stdout "AUTH.3 signed-out logout" 0 "Already signed out" tb auth logout
run_test_json "AUTH.4 signed-out logout --json" tb --json auth logout
run_test_stdout "AUTH.5 signed-out logout --local-only" 0 "Already signed out" tb auth logout --local-only
echo ""

if [ "$AUTH_ONLY" = "1" ]; then
    print_summary
    exit $?
fi

if [ "$AUTH_ONLY" != "0" ]; then
    echo "❌ AUTH_ONLY must be 0 or 1."
    exit 2
fi

if [ -z "${TTSBUDDY_API_KEY:-}" ]; then
    echo "❌ TTSBUDDY_API_KEY not set. Export it first, or use AUTH_ONLY=1."
    exit 2
fi

# --- Seed config ---

echo "📋 Setup"
tb config set key "$TTSBUDDY_API_KEY" >/dev/null 2>&1
tb config set voice st_m1 >/dev/null 2>&1
tb config set speed 1.25 >/dev/null 2>&1
tb config set timeout 30s >/dev/null 2>&1
tb config set output_dir "$TB_OUT" >/dev/null 2>&1
echo "  Config seeded in $TB_HOME"

# Create markdown fixture
cat > "$TB_OUT/test.md" <<'MDEOF'
# Test Document
This is **bold** with a [link](https://example.com).
The end.
MDEOF
echo "  Markdown fixture created"
echo ""

# ============================================================
# A. Command Surface, Help, and Docs
# ============================================================

echo "📋 A. Command Surface"
run_test_stdout "A.1 version" 0 "ttsbuddy" tb version
run_test_json   "A.2 version --json" tb version --json
run_test_stdout "A.3 --version" 0 "ttsbuddy" tb --version
run_test_stdout "A.4 --help" 0 "access" tb --help
run_test_stdout "A.5 speak --help" 0 "idempotency-key" tb speak --help
run_test_stdout "A.5b web --help" 0 "webpage" tb web --help
run_test_stdout "A.5c access --help" 0 "buy" tb access --help
run_test "A.6a status --help" 0 tb status --help
run_test "A.6b voices --help" 0 tb voices --help
run_test "A.6c config --help" 0 tb config --help
run_test "A.6d completion --help" 0 tb completion --help

set +e
tb completion zsh >"$TB_OUT/_stdout" 2>/dev/null
local_exit=$?
set -e
if [ "$local_exit" -eq 0 ] && [ -s "$TB_OUT/_stdout" ]; then
    pass "A.7 completion zsh"
else
    fail "A.7 completion zsh" "exit $local_exit or empty output"
fi

skip "A.8 doc check" "manual — compare README against actual behavior"
echo ""

# ============================================================
# B. Config and Precedence
# ============================================================

echo "📋 B. Config and Precedence"
run_test_stdout "B.1 config" 0 "1.25" tb config
run_test_json   "B.2 config --json" tb config --json
run_test_stdout "B.3 config get key" 0 "_..." tb config get key
run_test_stdout "B.4 config get voice" 0 "st_m1" tb config get voice
run_test_stdout "B.5 config get speed" 0 "1.25" tb config get speed
run_test_stdout "B.6 config get timeout" 0 "30s" tb config get timeout

# B.7: set voice, verify, reset
tb config set voice bf_emma >/dev/null 2>&1
set +e
val=$(tb config get voice 2>/dev/null)
set -e
if [ "$val" = "bf_emma" ]; then pass "B.7 set/get voice"; else fail "B.7 set/get voice" "got '$val'"; fi
tb config set voice st_m1 >/dev/null 2>&1

# B.8: set speed, verify, reset
tb config set speed 0.9 >/dev/null 2>&1
set +e
val=$(tb config get speed 2>/dev/null)
set -e
if [ "$val" = "0.9" ]; then pass "B.8 set/get speed"; else fail "B.8 set/get speed" "got '$val'"; fi
tb config set speed 1.25 >/dev/null 2>&1

run_test "B.9 speed 3.0" 2 tb config set speed 3.0
run_test "B.10 speed junk" 2 tb config set speed 1.0junk
run_test "B.11 timeout garbage" 2 tb config set timeout garbage
run_test "B.12 unknown key" 2 tb config set bogus value
run_test "B.13 bad key format" 2 tb config set key badformat
run_test "B.14 get nonexistent" 2 tb config get nonexistent

# B.15: env override
set +e
val=$(TTSBUDDY_VOICE=bf_emma tb config get voice 2>/dev/null)
set -e
if [ "$val" = "bf_emma" ]; then pass "B.15 env override"; else fail "B.15 env override" "got '$val'"; fi

# B.16: fresh home smoke
FRESH_HOME="$(mktemp -d /tmp/ttsbuddy-fresh.XXXXXX)"
set +e
HOME="$FRESH_HOME" "$BINARY" version >/dev/null 2>&1 && \
HOME="$FRESH_HOME" "$BINARY" --help >/dev/null 2>&1 && \
HOME="$FRESH_HOME" "$BINARY" voices >/dev/null 2>&1 && \
HOME="$FRESH_HOME" "$BINARY" completion zsh >/dev/null 2>&1
fresh_exit=$?
set -e
rm -rf "$FRESH_HOME"
if [ "$fresh_exit" -eq 0 ]; then pass "B.16 fresh home"; else fail "B.16 fresh home" "exit $fresh_exit"; fi
echo ""

# ============================================================
# C. Voices
# ============================================================

echo "📋 C. Voices"

set +e
tb voices >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
voices_exit=$?
set -e
if [ "$voices_exit" -eq 0 ]; then
    count=$(tail -n +3 "$TB_OUT/_stdout" | wc -l | tr -d ' ')
    if [ "$count" -ge 300 ] \
        && grep -q "st_f1" "$TB_OUT/_stdout" \
        && grep -q "Ava" "$TB_OUT/_stdout" \
        && grep -q "Louis" "$TB_OUT/_stdout" \
        && ! grep -Eq '(^|[[:space:]])(F1|M1)([[:space:]]|$)' "$TB_OUT/_stdout"; then
        pass "C.1 voices (native Fast names)"
    else
        fail "C.1 voices" "expected 300+ rows with native Fast names and no raw F1/M1 labels, got $count"
    fi
else
    fail "C.1 voices" "exit $voices_exit"
fi

set +e
tb voices --json >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
set -e
if jq -e '
    length >= 300
    and any(.[]; .id == "st_f1" and .name == "Ava" and .language_code == "en")
    and any(.[]; .id == "st_m1" and .name == "Louis" and .language_code == "fr")
    and all(.[]; .name != "F1" and .name != "M1")
' "$TB_OUT/_stdout" >/dev/null 2>&1; then
    pass "C.2 voices --json (native Fast names)"
else
    fail "C.2 voices --json" "invalid JSON or missing native Fast names"
fi

# C.3/C.4: live catalog — may fail if API is down
set +e
tb voices --all >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
c3_exit=$?
set -e
if [ "$c3_exit" -eq 0 ]; then
    count=$(tail -n +3 "$TB_OUT/_stdout" | wc -l | tr -d ' ')
    if [ "$count" -ge 300 ] \
        && grep -q "st_f1" "$TB_OUT/_stdout" \
        && grep -q "Ava" "$TB_OUT/_stdout" \
        && grep -q "Louis" "$TB_OUT/_stdout" \
        && ! grep -Eq '(^|[[:space:]])(F1|M1)([[:space:]]|$)' "$TB_OUT/_stdout"; then
        pass "C.3 voices --all (native Fast names)"
    else
        fail "C.3 voices --all" "expected 300+ rows with native Fast names and no raw F1/M1 labels, got $count voices"
    fi
else
    fail "C.3 voices --all" "exit non-zero"
fi

set +e
tb voices --all --json >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
set -e
if jq -e '
    length >= 300
    and any(.[]; .id == "st_f1" and .name == "Ava" and .language_code == "en")
    and any(.[]; .id == "st_m1" and .name == "Louis" and .language_code == "fr")
    and all(.[]; .name != "F1" and .name != "M1")
' "$TB_OUT/_stdout" >/dev/null 2>&1; then
    stderr_content=$(cat "$TB_OUT/_stderr")
    if [ -z "$stderr_content" ]; then
        pass "C.4 voices --all --json (no stderr)"
    else
        fail "C.4 voices --all --json" "stderr not empty: $stderr_content"
    fi
else
    fail "C.4 voices --all --json" "invalid JSON or missing native Fast names"
fi
echo ""

# ============================================================
# D. Non-POST Input Validation
# ============================================================

echo "📋 D. Input Validation"
run_test "D.1 no input" 2 tb speak
run_test "D.2 json + -o -" 2 tb speak --json -o - "test"
run_test "D.3 file not found" 2 tb speak -f /nonexistent
run_test "D.4 not regular file" 2 tb speak -f /tmp
echo ""

# ============================================================
# P. POST Tests (rate-limited, ~65s between each)
# ============================================================

# 5 POST tests covering distinct code paths. Server rate limit is 1 POST/min,
# so pacing must be >= 60s. Each test uses minimal text for fast TTS.
echo "📋 P. POST Tests (${POST_DELAY}s pacing, 5 tests)"
echo "  ⚠️  Estimated ~$((POST_DELAY * 5))s (~$((POST_DELAY * 5 / 60))min)"

# P.1: file save + --quiet (covers: file output, --quiet suppresses stderr)
post_test "P.1 speak → file (quiet)" 0 tb speak "Hi" -o "$TB_OUT/t1.mp3" --quiet
stderr_content=$(cat "$TB_OUT/_stderr" 2>/dev/null)
if [ -s "$TB_OUT/t1.mp3" ] && [ -z "$stderr_content" ]; then
    pass "P.1 file exists + quiet"
else
    fail "P.1 file exists + quiet" "file=$(ls -la "$TB_OUT/t1.mp3" 2>&1) stderr='$stderr_content'"
fi

# P.2: JSON output (covers: --json mode, captures job_id for status tests)
post_test "P.2 speak --json" 0 tb speak "Ok" --json
cp "$TB_OUT/_stdout" "$TB_OUT/t2.json"
if jq -e '.status' "$TB_OUT/t2.json" >/dev/null 2>&1; then
    JOB_ID=$(jq -r '.job_id' "$TB_OUT/t2.json")
    pass "P.2 valid JSON (job=$JOB_ID)"
else
    fail "P.2 valid JSON" "missing .status"
fi

# P.3: stdin pipe + --no-download (covers: stdin input, URL-only output)
echo "    ⏳ waiting ${POST_DELAY}s..."
sleep "$POST_DELAY"
set +e
printf 'Go\n' | tb speak - --no-download >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
p3_exit=$?
set -e
if [ "$p3_exit" -ne 0 ] && grep -q "rate limited" "$TB_OUT/_stderr" 2>/dev/null; then
    echo "    🔄 rate limited, retrying after ${POST_DELAY}s..."
    RATE_LIMITED_COUNT=$((RATE_LIMITED_COUNT + 1))
    sleep "$POST_DELAY"
    set +e
    printf 'Go\n' | tb speak - --no-download >"$TB_OUT/_stdout" 2>"$TB_OUT/_stderr"
    p3_exit=$?
    set -e
fi
if [ "$p3_exit" -eq 0 ]; then
    pass "P.3 stdin + no-download"
    if grep -q "https://" "$TB_OUT/_stderr" 2>/dev/null; then
        pass "P.3 URL on stderr"
    else
        fail "P.3 URL on stderr" "no URL found"
    fi
else
    fail "P.3 stdin + no-download" "exit $p3_exit"
fi

# P.4: markdown file (covers: -f flag, .md preprocessing)
post_test "P.4 speak -f .md" 0 tb speak -f "$TB_OUT/test.md" -o "$TB_OUT/t4.mp3"
if [ -s "$TB_OUT/t4.mp3" ]; then pass "P.4 md file exists"; else fail "P.4 md file exists" "missing"; fi

# P.5: stdout + custom voice/speed (covers: -o -, -v, -s, --idempotency-key)
echo "    ⏳ waiting ${POST_DELAY}s..."
sleep "$POST_DELAY"
set +e
tb speak "Yo" -v bf_emma -s 0.7 --idempotency-key "accept-test-${ACCEPTANCE_RUN_ID}-p5" -o - >"$TB_OUT/t5.mp3" 2>"$TB_OUT/_stderr"
p5_exit=$?
set -e
if [ "$p5_exit" -ne 0 ] && grep -q "rate limited" "$TB_OUT/_stderr" 2>/dev/null; then
    echo "    🔄 rate limited, retrying after ${POST_DELAY}s..."
    RATE_LIMITED_COUNT=$((RATE_LIMITED_COUNT + 1))
    sleep "$POST_DELAY"
    set +e
    tb speak "Yo" -v bf_emma -s 0.7 --idempotency-key "accept-test-${ACCEPTANCE_RUN_ID}-p5-retry" -o - >"$TB_OUT/t5.mp3" 2>"$TB_OUT/_stderr"
    p5_exit=$?
    set -e
fi
if [ "$p5_exit" -eq 0 ] && is_audio_file "$TB_OUT/t5.mp3"; then
    pass "P.5 stdout + voice + speed"
else
    fail "P.5 stdout + voice + speed" "exit $p5_exit"
fi
echo ""

# ============================================================
# W. Webpage Tests (live page, translation path)
# ============================================================

echo "📋 W. Webpage Tests"

RUSSIAN_VOICES=(st_m1 st_m2 st_m3 st_m4 st_m5 st_f1 st_f2 st_f3 st_f4 st_f5)
RANDOM_SUPERTONIC_VOICE="${RUSSIAN_VOICES[$((RANDOM % ${#RUSSIAN_VOICES[@]}))]}"
echo "    🎲 Russian target voice: $RANDOM_SUPERTONIC_VOICE"

echo "    ⏳ waiting ${POST_DELAY}s..."
sleep "$POST_DELAY"
set +e
tb web "https://www.ttsbuddy.com/docs/" --language ru --voice "$RANDOM_SUPERTONIC_VOICE" --json --no-download --timeout 10m >"$TB_OUT/web-ru.json" 2>"$TB_OUT/_stderr"
w1_exit=$?
set -e
if [ "$w1_exit" -ne 0 ] && grep -q "rate limited" "$TB_OUT/_stderr" 2>/dev/null; then
    echo "    🔄 rate limited, retrying after ${POST_DELAY}s..."
    RATE_LIMITED_COUNT=$((RATE_LIMITED_COUNT + 1))
    sleep "$POST_DELAY"
    set +e
    tb web "https://www.ttsbuddy.com/docs/" --language ru --voice "$RANDOM_SUPERTONIC_VOICE" --json --no-download --timeout 10m >"$TB_OUT/web-ru.json" 2>"$TB_OUT/_stderr"
    w1_exit=$?
    set -e
fi

if [ "$w1_exit" -eq 0 ] && jq -e '.audio_url or .status_url' "$TB_OUT/web-ru.json" >/dev/null 2>&1; then
    pass "W.1 docs webpage to Russian"
else
    fail "W.1 docs webpage to Russian" "exit $w1_exit or missing audio/status URL"
fi

if jq -e '.meta.target_language == "ru"' "$TB_OUT/web-ru.json" >/dev/null 2>&1; then
    pass "W.2 target language metadata"
elif jq -e '.meta.target_language == null' "$TB_OUT/web-ru.json" >/dev/null 2>&1; then
    skip "W.2 target language metadata" "backend did not include optional webpage metadata"
else
    fail "W.2 target language metadata" "target language was not ru"
fi

if jq -e '.meta.translated == true' "$TB_OUT/web-ru.json" >/dev/null 2>&1; then
    pass "W.3 translation metadata"
elif jq -e '.meta.translated == null' "$TB_OUT/web-ru.json" >/dev/null 2>&1; then
    skip "W.3 translation metadata" "backend did not include optional translation metadata"
else
    fail "W.3 translation metadata" "translated was not true"
fi
echo ""

# ============================================================
# S. Status Tests (GET only, no pacing needed)
# ============================================================

echo "📋 S. Status Tests"

run_test "S.1 status (last job)" 0 tb status
run_test_json "S.2 status --json" tb status --json

if [ -n "$JOB_ID" ]; then
    run_test "S.3 status <id>" 0 tb status "$JOB_ID"
    run_test "S.4 status --watch" 0 tb status "$JOB_ID" --watch --timeout 30s
else
    skip "S.3 status <id>" "no job_id from P.2"
    skip "S.4 status --watch" "no job_id from P.2"
fi

run_test "S.5 status not found" 1 tb status 00000000-0000-0000-0000-000000000000
echo ""

# ============================================================
# Artifact Verification
# ============================================================

echo "📋 Artifact Verification"

# MP3 identification
if is_audio_file "$TB_OUT/t1.mp3"; then
    pass "V.1 t1.mp3 is audio"
else
    fail "V.1 t1.mp3 is audio" "$(file "$TB_OUT/t1.mp3" 2>&1)"
fi

if is_audio_file "$TB_OUT/t5.mp3"; then
    pass "V.2 t5.mp3 (stdout) is audio"
else
    fail "V.2 t5.mp3 (stdout) is audio" "$(file "$TB_OUT/t5.mp3" 2>&1)"
fi

# File listing
echo ""
echo "📁 Output files:"
ls -lh "$TB_OUT"/*.mp3 "$TB_OUT"/*.json 2>/dev/null | while read -r line; do echo "  $line"; done
echo ""

# ============================================================
# Summary
# ============================================================

print_summary
