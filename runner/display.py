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


def display_file(i: str, o: str, width: int, fmt: str, quality: int) -> str:
    """The display copy of one file; returns a one-line summary."""
    img = Image.open(i)
    img.load()
    if img.mode not in ("RGB", "RGBA"):
        img = img.convert("RGB")
    if width > 0 and img.width > width:
        h = round(img.height * width / img.width)
        img = img.resize((width, h), Image.LANCZOS)
    fmt = fmt.lower()
    if fmt == "webp":
        img.save(o, "WEBP", quality=quality, method=6)
    elif fmt in ("jpg", "jpeg"):
        if img.mode == "RGBA":
            img = img.convert("RGB")
        img.save(o, "JPEG", quality=quality, optimize=True, progressive=True)
    else:
        raise ValueError(f"unsupported format {fmt}")
    return f"{i} -> {o} ({img.width}x{img.height}, {fmt} q{quality})"


def main() -> int:
    a = parse()
    try:
        print(display_file(a.i, a.o, a.w, a.f, a.q), file=sys.stderr)
    except ValueError as e:
        print(str(e), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
