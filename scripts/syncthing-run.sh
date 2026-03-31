#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

synctimething::init_env
if [[ ! -f "$SYNCTIMETHING_DEV_SYNCTHING_HOME/config.xml" ]]; then
  synctimething::reset_syncthing_state
fi

exec env HOME="$SYNCTIMETHING_DEV_SYNCTHING_USER_HOME" \
  syncthing serve \
    --home "$SYNCTIMETHING_DEV_SYNCTHING_HOME" \
    --gui-address "$SYNCTIMETHING_DEV_SYNCTHING_URL" \
    --gui-apikey "$SYNCTIMETHING_DEV_SYNCTHING_API_KEY" \
    --no-port-probing \
    --no-browser \
    --no-restart \
    --no-upgrade \
    --log-file=-
