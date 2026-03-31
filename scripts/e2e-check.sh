#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/synctimething-e2e-check.XXXXXX")"
cookiejar="$tmpdir/cookies.txt"
headers="$tmpdir/headers.txt"
page="$tmpdir/page.html"
device_id=""

cleanup() {
  if [[ -n "$device_id" ]]; then
    curl -fsS -X PATCH -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" -H "Content-Type: application/json" \
      -d '{"paused":false}' "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/config/devices/$device_id" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmpdir"
}
trap cleanup EXIT

synctimething::wait_for_syncthing 40
synctimething::wait_for_app 40

device_id="$(synctimething::first_device_id)"
if [[ -z "$device_id" || "$device_id" == "null" ]]; then
  echo "failed to discover a Syncthing device id" >&2
  exit 1
fi
if [[ "$(synctimething::device_paused "$device_id")" != "false" ]]; then
  echo "expected $device_id to start unpaused" >&2
  exit 1
fi

synctimething::login "$cookiejar" "$headers"

curl -fsS -o /dev/null -D "$headers" -b "$cookiejar" -c "$cookiejar" \
  --data-urlencode "syncthing_url=$SYNCTIMETHING_DEV_SYNCTHING_URL" \
  --data-urlencode "syncthing_api_key=$SYNCTIMETHING_DEV_SYNCTHING_API_KEY" \
  --data-urlencode "timezone=$SYNCTIMETHING_DEV_TIMEZONE" \
  "$SYNCTIMETHING_DEV_APP_URL/settings"
synctimething::assert_redirect "$headers" "/settings?saved=1"
curl -fsS -b "$cookiejar" "$SYNCTIMETHING_DEV_APP_URL/settings?saved=1" | grep -Fq "Settings saved and Syncthing connection verified."

schedule_epoch=$(( $(date -u +%s) + 90 ))
schedule="$(date -u -d "@$schedule_epoch" '+%M %H * * *')"

curl -fsS -o /dev/null -D "$headers" -b "$cookiejar" -c "$cookiejar" \
  --data-urlencode "name=Pause local device" \
  --data-urlencode "schedule=$schedule" \
  --data-urlencode "action=pause" \
  --data-urlencode "target_kind=device" \
  --data-urlencode "target_id=$device_id" \
  --data-urlencode "enabled=on" \
  "$SYNCTIMETHING_DEV_APP_URL/rules"
synctimething::assert_redirect "$headers" "/rules?saved=created"

synctimething::wait_for_device_paused "$device_id" true 150

if [[ ! -f "$SYNCTIMETHING_DEV_APP_DB_PATH" ]]; then
  echo "expected app database at $SYNCTIMETHING_DEV_APP_DB_PATH" >&2
  exit 1
fi

sqlite3 "$SYNCTIMETHING_DEV_APP_DB_PATH" "SELECT status || ':' || message FROM rule_runs ORDER BY id DESC LIMIT 1;" | grep -Fxq "success:executed"
curl -fsS -b "$cookiejar" "$SYNCTIMETHING_DEV_APP_URL/dashboard" >"$page"
grep -Fq "Pause local device" "$page"
grep -Fq "executed" "$page"

echo "E2E checks passed."
