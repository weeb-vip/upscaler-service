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


def providers() -> list:
    """The execution providers, from UPSCALER_COREML_UNITS. Unset is the CPU,
    which is what the cluster has. On a Mac, CPUAndNeuralEngine runs the
    network on the Neural Engine: measured on an M1 Max, a 225px poster went
    from 6.6s on one CPU thread to 0.55s, and eight processes sharing it did
    64 images in 15s against a CPU that was busy with other work. CPUAndGPU
    and ALL are accepted too; the CPU is always the fallback for any part
    Core ML will not take."""
    units = os.environ.get("UPSCALER_COREML_UNITS", "")
    if not units or "CoreMLExecutionProvider" not in ort.get_available_providers():
        return ["CPUExecutionProvider"]
    return [("CoreMLExecutionProvider", {"ModelFormat": "MLProgram", "MLComputeUnits": units}), "CPUExecutionProvider"]


def session(model_path: str, threads: int) -> ort.InferenceSession:
    opts = ort.SessionOptions()
    opts.log_severity_level = 3
    if threads > 0:
        opts.intra_op_num_threads = threads
        opts.inter_op_num_threads = 1
    return ort.InferenceSession(model_path, opts, providers=providers())


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


_sessions = {}


def cached_session(model_path: str, threads: int) -> ort.InferenceSession:
    """One session per model and thread count, kept for the life of the
    process: loading the network is most of a short run's cost, and serve.py
    answers many requests from one process."""
    key = (model_path, threads)
    if key not in _sessions:
        _sessions[key] = session(model_path, threads)
    return _sessions[key]


def threads_of(spec: str) -> int:
    if not spec:
        return 0
    parts = spec.split(":")
    return int(parts[1] if len(parts) == 3 else parts[0])


def upscale_file(i: str, o: str, model: str, models_dir: str, scale: int, tile: int, threads: int, fmt: str) -> str:
    """Upscale one file into another; returns a one-line summary. Raises on
    a missing model or an all-black result."""
    start = time.time()
    model_path = os.path.join(models_dir, model + ".onnx")
    if not os.path.exists(model_path):
        raise FileNotFoundError(f"model not found: {model_path}")
    tile = tile if tile > 0 else 256
    src = Image.open(i).convert("RGB")
    img = np.asarray(src, dtype=np.float32) / 255.0
    sess = cached_session(model_path, threads)
    out = np.nan_to_num(run_tiled(sess, img, tile), nan=0.0, posinf=1.0, neginf=0.0)
    if img.max() > 0.02 and out.max() <= 0.02:
        raise RuntimeError("upscale produced an all-black image")
    result = Image.fromarray(np.clip(out * 255.0 + 0.5, 0, 255).astype(np.uint8))
    if scale != NATIVE_SCALE:
        result = result.resize((src.width * scale, src.height * scale), Image.LANCZOS)
    fmt = (fmt or os.path.splitext(o)[1].lstrip(".") or "png").lower()
    if fmt in ("jpg", "jpeg"):
        result.save(o, "JPEG", quality=92)
    elif fmt == "webp":
        result.save(o, "WEBP", quality=92)
    else:
        result.save(o, "PNG")
    return f"{i} -> {o} ({model}, x{scale}, tile {tile}, {time.time() - start:.1f}s)"


def main() -> int:
    a = parse()
    try:
        summary = upscale_file(a.i, a.o, a.n, a.m, a.s, a.t, threads_of(a.j), a.f)
    except FileNotFoundError as e:
        print(str(e), file=sys.stderr)
        return 2
    except RuntimeError as e:
        print(str(e), file=sys.stderr)
        return 3
    if a.v:
        print(summary, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
