#!/usr/bin/env bash
set -euo pipefail

INSTALL_TARGET="${INSTALL_TARGET:-./cmd/ttsbuddy}"
EXPECTED_VERSION="${EXPECTED_VERSION:-}"

install_root="$(mktemp -d)"
trap 'rm -rf "$install_root"' EXIT

GOBIN="$install_root/bin" go install "$INSTALL_TARGET"

binary="$install_root/bin/ttsbuddy"
if [[ ! -x "$binary" ]]; then
  echo "expected go install to create $binary" >&2
  exit 1
fi
if [[ -e "$install_root/bin/ttsbuddy-cli" ]]; then
  echo "go install unexpectedly created ttsbuddy-cli" >&2
  exit 1
fi

version_output="$($binary version)"
if [[ "$version_output" != ttsbuddy\ * ]]; then
  echo "version banner must start with 'ttsbuddy ': $version_output" >&2
  exit 1
fi

if [[ -n "$EXPECTED_VERSION" ]]; then
  if [[ "$version_output" != "ttsbuddy $EXPECTED_VERSION "* ]]; then
    echo "version banner does not report $EXPECTED_VERSION: $version_output" >&2
    exit 1
  fi
  if [[ "$version_output" == *" dev "* ]]; then
    echo "tagged installation reported development metadata: $version_output" >&2
    exit 1
  fi
fi

echo "go install regression passed: $version_output"
