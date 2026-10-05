"""
Export a Real-ESRGAN SRVGG checkpoint (.pth) to ONNX, with a dynamic
spatial size so one graph serves every tile.

    python convert.py realesr-general-x4v3.pth realesr-general-x4v3.onnx
"""
import sys
from pathlib import Path

import torch

from srvgg import KNOWN, SRVGGNetCompact


def main(src: str, dst: str) -> None:
    name = Path(src).stem
    if name not in KNOWN:
        raise SystemExit(f"{name}: not a known SRVGG checkpoint ({', '.join(KNOWN)})")
    model = SRVGGNetCompact(**KNOWN[name])
    state = torch.load(src, map_location="cpu")
    state = state.get("params_ema", state.get("params", state))
    model.load_state_dict(state, strict=True)
    model.eval()
    example = torch.rand(1, 3, 64, 64)
    torch.onnx.export(
        model,
        example,
        dst,
        input_names=["input"],
        output_names=["output"],
        dynamic_axes={"input": {0: "n", 2: "h", 3: "w"}, "output": {0: "n", 2: "h", 3: "w"}},
        opset_version=17,
        dynamo=False,
    )
    print(f"{src} -> {dst}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
