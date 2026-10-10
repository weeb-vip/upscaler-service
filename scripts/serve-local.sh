#!/bin/sh
# runner/serve.py through this repo's own virtualenv, for a local walk:
# UPSCALER_RUNNER_SERVE=$PWD/scripts/serve-local.sh
exec "$(dirname "$0")/../.venv/bin/python" "$(dirname "$0")/../runner/serve.py" "$@"
