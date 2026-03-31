#!/usr/bin/env bash

synctimething::project_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

synctimething::init_env() {
  export SYNCTIMETHING_PROJECT_ROOT="${SYNCTIMETHING_PROJECT_ROOT:-$(synctimething::project_root)}"
  export SYNCTIMETHING_DEV_STATE_DIR="${SYNCTIMETHING_DEV_STATE_DIR:-$SYNCTIMETHING_PROJECT_ROOT/.devenv/state}"
  export SYNCTIMETHING_DEV_LOG_DIR="${SYNCTIMETHING_DEV_LOG_DIR:-$SYNCTIMETHING_DEV_STATE_DIR/logs}"
  export SYNCTIMETHING_DEV_APP_DATA_DIR="${SYNCTIMETHING_DEV_APP_DATA_DIR:-$SYNCTIMETHING_DEV_STATE_DIR/harness-app}"
  export SYNCTIMETHING_DEV_APP_ADDR="${SYNCTIMETHING_DEV_APP_ADDR:-127.0.0.1:18080}"
  export SYNCTIMETHING_DEV_APP_URL="${SYNCTIMETHING_DEV_APP_URL:-http://$SYNCTIMETHING_DEV_APP_ADDR}"
  export SYNCTIMETHING_DEV_APP_DB_PATH="${SYNCTIMETHING_DEV_APP_DB_PATH:-$SYNCTIMETHING_DEV_APP_DATA_DIR/sync-time-thing.db}"
  export SYNCTIMETHING_DEV_ADMIN_USERNAME="${SYNCTIMETHING_DEV_ADMIN_USERNAME:-admin}"
  export SYNCTIMETHING_DEV_ADMIN_PASSWORD="${SYNCTIMETHING_DEV_ADMIN_PASSWORD:-devenv-admin-password}"
  export SYNCTIMETHING_DEV_TIMEZONE="${SYNCTIMETHING_DEV_TIMEZONE:-UTC}"
  export SYNCTIMETHING_DEV_ENCRYPTION_KEY="${SYNCTIMETHING_DEV_ENCRYPTION_KEY:-MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=}"
  export SYNCTIMETHING_DEV_SYNCTHING_HOME="${SYNCTIMETHING_DEV_SYNCTHING_HOME:-$SYNCTIMETHING_DEV_STATE_DIR/harness-syncthing}"
  export SYNCTIMETHING_DEV_SYNCTHING_USER_HOME="${SYNCTIMETHING_DEV_SYNCTHING_USER_HOME:-$SYNCTIMETHING_DEV_STATE_DIR/harness-syncthing-home}"
  export SYNCTIMETHING_DEV_SYNCTHING_URL="${SYNCTIMETHING_DEV_SYNCTHING_URL:-http://127.0.0.1:18484}"
  export SYNCTIMETHING_DEV_SYNCTHING_API_KEY="${SYNCTIMETHING_DEV_SYNCTHING_API_KEY:-synctimething-dev-syncthing-key}"
  export SYNCTIMETHING_DEV_SYNCTHING_FOLDER_PATH="${SYNCTIMETHING_DEV_SYNCTHING_FOLDER_PATH:-$SYNCTIMETHING_DEV_SYNCTHING_USER_HOME/Sync}"
  mkdir -p "$SYNCTIMETHING_DEV_LOG_DIR"
}

synctimething::reset_app_state() {
  rm -rf "$SYNCTIMETHING_DEV_APP_DATA_DIR"
  mkdir -p "$SYNCTIMETHING_DEV_APP_DATA_DIR"
}

synctimething::reset_syncthing_state() {
  rm -rf "$SYNCTIMETHING_DEV_SYNCTHING_HOME" "$SYNCTIMETHING_DEV_SYNCTHING_USER_HOME"
  mkdir -p "$SYNCTIMETHING_DEV_SYNCTHING_USER_HOME"
  HOME="$SYNCTIMETHING_DEV_SYNCTHING_USER_HOME" syncthing generate --home "$SYNCTIMETHING_DEV_SYNCTHING_HOME" --no-port-probing >/dev/null
  mkdir -p "$SYNCTIMETHING_DEV_SYNCTHING_FOLDER_PATH"
}

synctimething::cleanup_pid() {
  local pid="$1"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  fi
}

synctimething::listener_pid() {
  local port="$1"
  if ! command -v ss >/dev/null 2>&1; then
    return 0
  fi
  ss -ltnp "sport = :$port" 2>/dev/null | awk '
    {
      if (match($0, /pid=[0-9]+/)) {
        print substr($0, RSTART + 4, RLENGTH - 4)
        exit
      }
    }
  '
}

synctimething::stop_listener() {
  local addr="$1"
  local port="${addr##*:}"
  local pid
  pid="$(synctimething::listener_pid "$port")"
  if [[ -n "$pid" ]]; then
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  fi
}

synctimething::start_background() {
  local logfile="$1"
  shift
  "$@" >"$logfile" 2>&1 &
  echo "$!"
}

synctimething::wait_for_app() {
  local attempts="${1:-40}"
  local i
  for i in $(seq 1 "$attempts"); do
    if curl -fsS "$SYNCTIMETHING_DEV_APP_URL/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "app did not become healthy at $SYNCTIMETHING_DEV_APP_URL" >&2
  return 1
}

synctimething::wait_for_syncthing() {
  local attempts="${1:-40}"
  local i
  for i in $(seq 1 "$attempts"); do
    if curl -fsS -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/system/ping" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "syncthing did not become healthy at $SYNCTIMETHING_DEV_SYNCTHING_URL" >&2
  return 1
}

synctimething::assert_redirect() {
  local headers_file="$1"
  local location="$2"
  grep -Eq '^HTTP/.* 303' "$headers_file"
  grep -Fq "Location: $location" "$headers_file"
}

synctimething::login() {
  local cookiejar="$1"
  local headers_file="$2"
  curl -fsS -o /dev/null -D "$headers_file" -c "$cookiejar" \
    --data-urlencode "username=$SYNCTIMETHING_DEV_ADMIN_USERNAME" \
    --data-urlencode "password=$SYNCTIMETHING_DEV_ADMIN_PASSWORD" \
    "$SYNCTIMETHING_DEV_APP_URL/login"
  synctimething::assert_redirect "$headers_file" "/dashboard"
}

synctimething::first_folder_id() {
  curl -fsS -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/config/folders" \
    | jq -r '.[0].id'
}

synctimething::folder_paused() {
  local folder_id="$1"
  curl -fsS -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/config/folders" \
    | jq -r --arg id "$folder_id" '.[] | select(.id == $id) | .paused'
}

synctimething::wait_for_folder_paused() {
  local folder_id="$1"
  local expected="$2"
  local attempts="${3:-120}"
  local i
  for i in $(seq 1 "$attempts"); do
    if [[ "$(synctimething::folder_paused "$folder_id")" == "$expected" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "folder $folder_id did not reach paused=$expected" >&2
  return 1
}

synctimething::first_device_id() {
  curl -fsS -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/config/devices" \
    | jq -r '.[0].deviceID'
}

synctimething::device_paused() {
  local device_id="$1"
  curl -fsS -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/config/devices" \
    | jq -r --arg id "$device_id" '.[] | select(.deviceID == $id) | .paused'
}

synctimething::wait_for_device_paused() {
  local device_id="$1"
  local expected="$2"
  local attempts="${3:-120}"
  local i
  for i in $(seq 1 "$attempts"); do
    if [[ "$(synctimething::device_paused "$device_id")" == "$expected" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "device $device_id did not reach paused=$expected" >&2
  return 1
}
