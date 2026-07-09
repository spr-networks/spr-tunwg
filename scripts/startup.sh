#!/bin/bash
set -a
. /configs/base/config.sh
if [ -f /configs/spr-tunwg/config.sh ]; then
    . /configs/spr-tunwg/config.sh
fi
set +a

# tunwg child processes keep their WireGuard private keys here
# (TUNWG_PATH/keys/<TUNWG_KEY>, written 0400 by tunwg).
mkdir -p /state/plugins/spr-tunwg/tunwg/keys
chmod 700 /state/plugins/spr-tunwg/tunwg /state/plugins/spr-tunwg/tunwg/keys

exec /tunwg_plugin
