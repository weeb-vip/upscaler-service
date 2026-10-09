#!/usr/bin/env python3
"""
The display copy of an image: resized down to a width (never up) and encoded
for the page. Pillow, so WebP is available everywhere the runner is.

    display.py -i in.jpg -o out.webp -w 600 -f webp -q 80

Lanczos for the downscale, metadata dropped, WebP at method 6 (slowest
encoder, smallest file; this runs once per image). A JPEG output is
progressive and optimised, for anything that still wants one.
"""
import argparse
import sys

from PIL import Image


def parse():
    p = argparse.ArgumentParser(add_help=False)
    p.add_argument("-i", required=True)
    p.add_argument("-o", required=True)
    p.add_argument("-w", type=int, default=0, help="max width; 0 keeps the size")
    p.add_argument("-f", default="webp")
    p.add_argument("-q", type=int, default=80)
    p.add_argument("-h", action="help")
    return p.parse_args()


def main() -> int:
    a = parse()
    img = Image.open(a.i)
    img.load()
    if img.mode not in ("RGB", "RGBA"):
        img = img.convert("RGB")
    if a.w > 0 and img.width > a.w:
        h = round(img.height * a.w / img.width)
        img = img.resize((a.w, h), Image.LANCZOS)
    fmt = a.f.lower()
    if fmt == "webp":
        img.save(a.o, "WEBP", quality=a.q, method=6)
    elif fmt in ("jpg", "jpeg"):
        if img.mode == "RGBA":
            img = img.convert("RGB")
        img.save(a.o, "JPEG", quality=a.q, optimize=True, progressive=True)
    else:
        print(f"unsupported format {a.f}", file=sys.stderr)
        return 2
    print(f"{a.i} -> {a.o} ({img.width}x{img.height}, {fmt} q{a.q})", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
