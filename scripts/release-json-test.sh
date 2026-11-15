#!/usr/bin/env bash
# Tests scripts/release-json.sh against v0.8.8's real SHA256SUMS: the file
# it writes is the one flake.nix reads, with each hash in the SRI form
# `nix hash convert --hash-algo sha256 --to sri` gives (worked out with it
# once, and written here, since ./run check runs where there may be no
# nix), and a SHA256SUMS missing an architecture, or a version that is not
# one, makes nothing. Run by ./run check.
set -euo pipefail
unset CDPATH

script=$(cd "$(dirname "$0")" && pwd)/release-json.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

cat >"$work/SHA256SUMS" <<'SUMS'
ad082e397a96e63438b952b04087ba4578a0444c65f03e69f35b74c2793f2497  consonos-linux-x64
7aa8cbdfa99f74ac8b4f3533e88573cc5148c1c3472ce16d73f979321eb8c1bf  consonos-linux-arm64
ff9bc9011eb88480737e2571701a13e66f5c8e8f49a4c6512b6139753738de4c  consonos.service
SUMS

cat >"$work/want.json" <<'JSON'
{
  "version": "0.8.8",
  "assets": {
    "x86_64-linux": {
      "url": "https://github.com/danielbodart/consonos/releases/download/v0.8.8/consonos-linux-x64",
      "hash": "sha256-rQguOXqW5jQ4uVKwQIe6RXigRExl8D5p81t0wnk/JJc="
    },
    "aarch64-linux": {
      "url": "https://github.com/danielbodart/consonos/releases/download/v0.8.8/consonos-linux-arm64",
      "hash": "sha256-eqjL36mfdKyLTzUz6IVzzFFIwcNHLOFtc/l5Mh64wb8="
    }
  }
}
JSON

if ! "$script" 0.8.8 "$work/SHA256SUMS" >"$work/got.json"; then
    fail "a release's SHA256SUMS: refused"
elif ! diff -u "$work/want.json" "$work/got.json"; then
    fail "a release's SHA256SUMS: not the release.json flake.nix wants"
fi

grep -v linux-arm64 "$work/SHA256SUMS" >"$work/short"
if "$script" 0.8.8 "$work/short" >/dev/null 2>&1; then
    fail "a SHA256SUMS without linux-arm64: accepted"
fi

if "$script" v0.8.8 "$work/SHA256SUMS" >/dev/null 2>&1; then
    fail "a version with a v: accepted"
fi

if [ $failures -gt 0 ]; then
    exit 1
fi
echo "release-json: ok"
