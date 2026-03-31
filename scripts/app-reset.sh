#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env
synctimething::stop_listener "$SYNCTIMETHING_DEV_APP_ADDR"
synctimething::reset_app_state

echo "App harness reset:"
echo "  data-dir: $SYNCTIMETHING_DEV_APP_DATA_DIR"
echo "  url: $SYNCTIMETHING_DEV_APP_URL"
