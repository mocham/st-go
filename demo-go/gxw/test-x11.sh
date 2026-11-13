#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
assets="$root/TEST-ASSETS/x11"
if [ -x "$assets/bin/Xvfb" ]; then
    export PATH="$assets/bin:$PATH"
fi
exec "$@"
