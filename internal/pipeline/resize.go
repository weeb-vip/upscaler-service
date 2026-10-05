package pipeline

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

// Display widths per kind: what the stored object is capped at. The CDN
// serves objects as they are (Cloudflare's resizer is off: its free tier
// lasted days), so the object at the key has to be a size a page can use.
// A 2720px poster at 5 MB is not; 1000px at ~150 KB is.
var DefaultDisplayWidths = map[Kind]int{
	KindAnime:     600,
	KindPoster:    1000,
	KindBanner:    1920,
	KindCharacter: 600,
	KindStaff:     600,
	KindWork:      1000,
}

// fitWidth scales an image down to at most maxWidth wide (never up) and
// encodes it as JPEG. Catmull-Rom: the best-looking of the stdlib-adjacent
// kernels for downscaling line art, and this runs once per image.
func fitWidth(src []byte, maxWidth int, quality int) ([]byte, int, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, 0, fmt.Errorf("decode: %w", err)
	}
	b := img.Bounds()
	if maxWidth <= 0 || b.Dx() <= maxWidth {
		return src, b.Dx(), nil
	}
	h := int(float64(b.Dy()) * float64(maxWidth) / float64(b.Dx()))
	dst := image.NewRGBA(image.Rect(0, 0, maxWidth, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, fmt.Errorf("encode: %w", err)
	}
	return out.Bytes(), maxWidth, nil
}
