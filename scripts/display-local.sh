#!/bin/sh
# runner/display.py through this repo's own virtualenv, for a local walk:
# UPSCALER_DISPLAY_BINARY=$PWD/scripts/display-local.sh
exec "$(dirname "$0")/../.venv/bin/python" "$(dirname "$0")/../runner/display.py" "$@"
