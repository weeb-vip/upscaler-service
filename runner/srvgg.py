"""
SRVGGNetCompact, the architecture behind Real-ESRGAN's realesr-general-x4v3
and realesr-animevideov3 weights: a stack of 3x3 conv + PReLU, a final conv
to scale*scale*3 channels, pixel shuffle, plus the nearest-upsampled input
as a residual. Written out here (it is forty lines) rather than imported
from basicsr, whose dependency pins have rotted.

Only needed at conversion time; the runtime uses the exported ONNX graph.
"""
import torch
import torch.nn as nn
import torch.nn.functional as F


class SRVGGNetCompact(nn.Module):
    def __init__(self, num_in_ch=3, num_out_ch=3, num_feat=64, num_conv=32, upscale=4, act_type="prelu"):
        super().__init__()
        self.upscale = upscale
        self.body = nn.ModuleList()
        self.body.append(nn.Conv2d(num_in_ch, num_feat, 3, 1, 1))
        self.body.append(self._act(act_type, num_feat))
        for _ in range(num_conv):
            self.body.append(nn.Conv2d(num_feat, num_feat, 3, 1, 1))
            self.body.append(self._act(act_type, num_feat))
        self.body.append(nn.Conv2d(num_feat, num_out_ch * upscale * upscale, 3, 1, 1))
        self.upsampler = nn.PixelShuffle(upscale)

    @staticmethod
    def _act(act_type, num_feat):
        if act_type == "relu":
            return nn.ReLU(inplace=True)
        if act_type == "prelu":
            return nn.PReLU(num_parameters=num_feat)
        if act_type == "leakyrelu":
            return nn.LeakyReLU(negative_slope=0.1, inplace=True)
        raise ValueError(act_type)

    def forward(self, x):
        out = x
        for layer in self.body:
            out = layer(out)
        out = self.upsampler(out)
        base = F.interpolate(x, scale_factor=self.upscale, mode="nearest")
        return out + base


# The published weights, by name: (num_feat, num_conv, upscale).
KNOWN = {
    "realesr-general-x4v3": dict(num_feat=64, num_conv=32, upscale=4),
    "realesr-general-wdn-x4v3": dict(num_feat=64, num_conv=32, upscale=4),
    "realesr-animevideov3": dict(num_feat=64, num_conv=16, upscale=4),
}
