#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env
synctimething::stop_listener "$SYNCTIMETHING_DEV_APP_ADDR"
synctimething::reset_app_state

app_log="$SYNCTIMETHING_DEV_LOG_DIR/smoke-app.log"
app_pid=""

cleanup() {
  synctimething::cleanup_pid "$app_pid"
}
trap cleanup EXIT

app_pid="$(synctimething::start_background "$app_log" bash "$SYNCTIMETHING_PROJECT_ROOT/scripts/app-run.sh")"
bash "$SYNCTIMETHING_PROJECT_ROOT/scripts/smoke-check.sh"
