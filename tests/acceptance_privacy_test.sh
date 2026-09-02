#!/usr/bin/env bash

set -euo pipefail

fixture_key="ttsb_TEST_FIXTURE_SECRET_NEVER_REAL"
stderr_sentinel="HARNESS_PRIVATE_STDERR_SENTINEL_7Q9X"
tmp_dir="$(mktemp -d /tmp/ttsbuddy-privacy-test.XXXXXX)"
trap 'rm -rf "$tmp_dir"' EXIT

fake_binary="$tmp_dir/ttsbuddy"
cat >"$fake_binary" <<'EOF'
#!/usr/bin/env bash

case "$*" in
    "auth --help")
        echo "login"
        ;;
    "auth status")
        echo "HARNESS_PRIVATE_STDERR_SENTINEL_7Q9X" >&2
        exit 2
        ;;
    "auth logout"|"auth logout --local-only")
        echo "Already signed out"
        ;;
    "--json auth logout")
        echo '{}'
        ;;
esac
EOF
chmod +x "$fake_binary"

output="$({
    AUTH_ONLY=1 \
        BINARY=/usr/bin/true \
        TTSBUDDY_API_KEY="$fixture_key" \
        ./tests/acceptance_test.sh
} 2>&1 || true)"

if [[ "$output" == *"${fixture_key:0:15}"* ]]; then
    echo "acceptance harness exposed API key material" >&2
    exit 1
fi

if [[ "$output" != *"API Key:    set (redacted)"* ]]; then
    echo "acceptance harness did not report a redacted API key" >&2
    exit 1
fi

failure_output="$({
    AUTH_ONLY=1 BINARY="$fake_binary" ./tests/acceptance_test.sh
} 2>&1 || true)"

if [[ "$failure_output" != *"AUTH.2 signed-out status: expected exit 1, got 2"* ]]; then
    echo "acceptance privacy fixture did not exercise the failure path" >&2
    exit 1
fi

if [[ "$failure_output" == *"$stderr_sentinel"* ]]; then
    echo "acceptance harness exposed raw command stderr" >&2
    exit 1
fi

echo "acceptance harness keeps credentials and raw stderr private"
