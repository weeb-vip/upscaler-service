package pipeline

import (
	"bytes"
	"image"
)

// blankSamples is the side of the grid isBlank samples: 64x64 points is
// enough to tell a black frame from a dark poster, and is the same cost
// whatever the image size.
const blankSamples = 64

// isBlank reports whether an image is (as good as) all black: every
// sampled pixel has no channel above 3/255. The ncnn Vulkan build of
// Real-ESRGAN writes exactly that when the device cannot allocate memory,
// and the lavapipe deployments of 1.5.0-1.7.0 did, with the original copy
// sitting untouched beside the key. A real poster, however dark, has
// highlights somewhere in a 64x64 grid.
func isBlank(data []byte) bool {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	if b.Empty() {
		return false
	}
	nx, ny := blankSamples, blankSamples
	if b.Dx() < nx {
		nx = b.Dx()
	}
	if b.Dy() < ny {
		ny = b.Dy()
	}
	const lit = 3 * 257 // 3/255 in the 16-bit range color.RGBA reports
	for j := 0; j < ny; j++ {
		y := b.Min.Y + j*b.Dy()/ny
		for i := 0; i < nx; i++ {
			x := b.Min.X + i*b.Dx()/nx
			r, g, bl, _ := img.At(x, y).RGBA()
			if r > lit || g > lit || bl > lit {
				return false
			}
		}
	}
	return true
}
