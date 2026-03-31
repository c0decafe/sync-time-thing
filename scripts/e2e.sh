#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env
synctimething::stop_listener "$SYNCTIMETHING_DEV_SYNCTHING_URL"
synctimething::stop_listener "$SYNCTIMETHING_DEV_APP_ADDR"
synctimething::reset_syncthing_state
synctimething::reset_app_state

syncthing_log="$SYNCTIMETHING_DEV_LOG_DIR/e2e-syncthing.log"
app_log="$SYNCTIMETHING_DEV_LOG_DIR/e2e-app.log"
syncthing_pid=""
app_pid=""

cleanup() {
  synctimething::cleanup_pid "$app_pid"
  synctimething::cleanup_pid "$syncthing_pid"
}
trap cleanup EXIT

syncthing_pid="$(synctimething::start_background "$syncthing_log" bash "$SYNCTIMETHING_PROJECT_ROOT/scripts/syncthing-run.sh")"
app_pid="$(synctimething::start_background "$app_log" bash "$SYNCTIMETHING_PROJECT_ROOT/scripts/app-run.sh")"
bash "$SYNCTIMETHING_PROJECT_ROOT/scripts/e2e-check.sh"
