#!/bin/sh
# The runner through this repo's own virtualenv (python3.10 + onnxruntime),
# for a local backfill: UPSCALER_BINARY=$PWD/scripts/upscale-local.sh. The
# Docker image runs runner/upscale.py directly on its system python.
exec "$(dirname "$0")/../.venv/bin/python" "$(dirname "$0")/../runner/upscale.py" "$@"
