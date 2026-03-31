#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/synctimething-smoke-check.XXXXXX")"
cookiejar="$tmpdir/cookies.txt"
headers="$tmpdir/headers.txt"

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

synctimething::wait_for_app 40

curl -fsS "$SYNCTIMETHING_DEV_APP_URL/login" | grep -Fq "Sign in"
synctimething::login "$cookiejar" "$headers"
curl -fsS -b "$cookiejar" "$SYNCTIMETHING_DEV_APP_URL/dashboard" | grep -Fq "Dashboard"
curl -fsS -b "$cookiejar" "$SYNCTIMETHING_DEV_APP_URL/settings" | grep -Fq "Settings"

echo "Smoke checks passed."
