#!/usr/bin/env python3
"""
The runner as a long-lived process: one JSON request per line on stdin, one
JSON answer per line on stdout. Starting Python, importing onnxruntime and
loading the network is most of a short job's cost; a process that stays up
pays it once.

    {"op": "upscale", "in": ..., "out": ..., "model": ..., "models_dir": ...,
     "scale": 2, "tile": 256, "threads": 1, "format": "png"}
    {"op": "display", "in": ..., "out": ..., "width": 600, "format": "webp", "quality": 80}
    -> {"ok": true, "summary": "..."} | {"ok": false, "error": "..."}

Anything the handlers print goes to stderr, so stdout carries only answers.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import display  # noqa: E402
import upscale  # noqa: E402


def handle(req: dict) -> str:
    op = req.get("op")
    if op == "upscale":
        return upscale.upscale_file(
            req["in"], req["out"], req.get("model", "realesr-general-x4v3"), req["models_dir"],
            int(req.get("scale", 2)), int(req.get("tile", 0)), int(req.get("threads", 0)), req.get("format", ""),
        )
    if op == "display":
        return display.display_file(req["in"], req["out"], int(req.get("width", 0)), req.get("format", "webp"), int(req.get("quality", 80)))
    if op == "ping":
        return "pong"
    raise ValueError(f"unknown op {op!r}")


def main() -> int:
    out = sys.stdout
    sys.stdout = sys.stderr
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            answer = {"ok": True, "summary": handle(json.loads(line))}
        except Exception as e:  # noqa: BLE001 -- every failure is an answer, never a dead process
            answer = {"ok": False, "error": f"{type(e).__name__}: {e}"}
        out.write(json.dumps(answer) + "\n")
        out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
