#!/usr/bin/env python3
"""
Real-ESRGAN on the CPU through ONNX Runtime, with the command line of
realesrgan-ncnn-vulkan so the Go service does not know the difference:

    upscale.py -i in.jpg -o out.jpg -n realesr-general-x4v3 -s 2 -m models -t 256 -j 1:4:1 -f jpg

The model's native factor is 4; a requested 2 or 3 is reached by scaling
the 4x result down with Lanczos, which is also what the ncnn build does.
Tiled with overlap, so memory is bounded by the tile, not the image.
"""
import argparse
import os
import sys
import time

import numpy as np
import onnxruntime as ort
from PIL import Image

NATIVE_SCALE = 4


def parse():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("-i", required=True)
    p.add_argument("-o", required=True)
    p.add_argument("-n", default="realesr-general-x4v3")
    p.add_argument("-s", type=int, default=4)
    p.add_argument("-m", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "models"))
    p.add_argument("-t", type=int, default=0)
    p.add_argument("-j", default="")
    p.add_argument("-f", default="")
    p.add_argument("-g", default="")  # accepted and ignored: there is no GPU here
    p.add_argument("-v", action="store_true")
    p.add_argument("-h", action="help")
    return p.parse_args()


def session(model_path: str, threads: int) -> ort.InferenceSession:
    opts = ort.SessionOptions()
    opts.log_severity_level = 3
    if threads > 0:
        opts.intra_op_num_threads = threads
        opts.inter_op_num_threads = 1
    return ort.InferenceSession(model_path, opts, providers=["CPUExecutionProvider"])


def run_tiled(sess, img: np.ndarray, tile: int, pad: int = 10) -> np.ndarray:
    """img: HxWx3 float32 in [0,1]. Returns (H*4)x(W*4)x3."""
    h, w, _ = img.shape
    out = np.zeros((h * NATIVE_SCALE, w * NATIVE_SCALE, 3), dtype=np.float32)
    name = sess.get_inputs()[0].name
    for y in range(0, h, tile):
        for x in range(0, w, tile):
            y0, x0 = max(y - pad, 0), max(x - pad, 0)
            y1, x1 = min(y + tile + pad, h), min(x + tile + pad, w)
            chunk = img[y0:y1, x0:x1]
            inp = np.ascontiguousarray(chunk.transpose(2, 0, 1)[None])
            res = sess.run(None, {name: inp})[0][0].transpose(1, 2, 0)
            # Trim the overlap back off, in output coordinates.
            ty, tx = (y - y0) * NATIVE_SCALE, (x - x0) * NATIVE_SCALE
            th, tw = (min(y + tile, h) - y) * NATIVE_SCALE, (min(x + tile, w) - x) * NATIVE_SCALE
            out[y * NATIVE_SCALE:y * NATIVE_SCALE + th, x * NATIVE_SCALE:x * NATIVE_SCALE + tw] = res[ty:ty + th, tx:tx + tw]
    return out


def main() -> int:
    a = parse()
    start = time.time()
    model_path = os.path.join(a.m, a.n + ".onnx")
    if not os.path.exists(model_path):
        print(f"model not found: {model_path}", file=sys.stderr)
        return 2
    threads = 0
    if a.j:
        parts = a.j.split(":")
        threads = int(parts[1] if len(parts) == 3 else parts[0])
    tile = a.t if a.t > 0 else 256

    src = Image.open(a.i).convert("RGB")
    img = np.asarray(src, dtype=np.float32) / 255.0
    sess = session(model_path, threads)
    out = run_tiled(sess, img, tile)
    result = Image.fromarray(np.clip(out * 255.0 + 0.5, 0, 255).astype(np.uint8))
    if a.s != NATIVE_SCALE:
        result = result.resize((src.width * a.s, src.height * a.s), Image.LANCZOS)

    fmt = (a.f or os.path.splitext(a.o)[1].lstrip(".") or "png").lower()
    if fmt in ("jpg", "jpeg"):
        result.save(a.o, "JPEG", quality=92)
    elif fmt == "webp":
        result.save(a.o, "WEBP", quality=92)
    else:
        result.save(a.o, "PNG")
    if a.v:
        print(f"{a.i} -> {a.o} ({a.n}, x{a.s}, tile {tile}, {time.time() - start:.1f}s)", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
