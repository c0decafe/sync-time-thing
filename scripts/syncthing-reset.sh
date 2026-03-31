#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env
synctimething::stop_listener "$SYNCTIMETHING_DEV_SYNCTHING_URL"
synctimething::reset_syncthing_state

echo "Syncthing harness reset:"
echo "  home: $SYNCTIMETHING_DEV_SYNCTHING_HOME"
echo "  user-home: $SYNCTIMETHING_DEV_SYNCTHING_USER_HOME"
echo "  url: $SYNCTIMETHING_DEV_SYNCTHING_URL"
echo "  api-key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY"
