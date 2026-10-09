package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"log"
	"strconv"
	"strings"
)

// Encoder is the one method of the display encoder the pipeline uses: the
// source resized down to `width` (0 keeps the size) in `format` at `quality`.
type Encoder interface {
	Encode(ctx context.Context, src []byte, width int, format string, quality int) ([]byte, error)
}

// Metadata of the display copies, beside MetaDisplayWidth.
const (
	// MetaDisplayFormat is the format the key holds (webp, jpg). An object
	// whose value differs from the configured format is re-encoded from
	// its best source on the next walk: that is how a bucket of JPEG
	// display copies becomes WebP without touching the originals.
	MetaDisplayFormat = "display-format"
	// MetaDisplayVariants lists the stored widths beside the key, e.g.
	// "320,640" for <key>-w320 and <key>-w640.
	MetaDisplayVariants = "display-variants"
)

// VariantSuffix builds the key of a stored width variant.
func VariantSuffix(width int) string { return "-w" + strconv.Itoa(width) }

// DefaultVariants per kind: the widths the pages actually draw. A poster
// card is 180-260 CSS px, so 320 serves it at 1x and the 600/1000 display
// copy at 2x; the hero rail draws 60px thumbnails, which 160 covers at 2x.
var DefaultVariants = map[Kind][]int{
	KindAnime:     {320},
	KindPoster:    {320, 640},
	KindWork:      {320, 640},
	KindCharacter: {160},
	KindStaff:     {160},
	KindBanner:    {960},
}

// displayStale reports whether an already-treated object holds its display
// copy in a format other than the configured one.
func (p *Pipeline) displayStale(meta map[string]string) bool {
	if p.opts.Encoder == nil || p.opts.DisplayFormat == "" {
		return false
	}
	return meta[MetaDisplayFormat] != p.opts.DisplayFormat
}

// bestSource is what a display copy should be made from: the widest
// picture available among the uncapped upscale at <key>-full, the object
// itself, and the untouched original at <key>-orig.
//
// Widest, not a fixed order. An upscale that fit under the cap was stored
// at the key with no -full beside it, and preferring -orig there handed the
// conversion the 225px original in place of the 450px result: 1,186 root
// keys came out of the first walk at a third of their size.
func (p *Pipeline) bestSource(ctx context.Context, key string, data []byte) ([]byte, error) {
	best, bestWidth := data, 0
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		bestWidth = cfg.Width
	}
	for _, suffix := range []string{p.opts.FullSuffix, p.opts.OrigSuffix} {
		if suffix == "" {
			continue
		}
		exists, err := p.store.Exists(ctx, key+suffix)
		if err != nil {
			return nil, fmt.Errorf("stat %s%s: %w", key, suffix, err)
		}
		if !exists {
			continue
		}
		src, _, _, err := p.store.Get(ctx, key+suffix)
		if err != nil {
			return nil, fmt.Errorf("get %s%s: %w", key, suffix, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
		if err != nil || isBlank(src) {
			continue
		}
		if cfg.Width > bestWidth {
			best, bestWidth = src, cfg.Width
		}
	}
	return best, nil
}

// renderDisplay writes the key's display copy from `source`, capped at
// `cap` wide, plus the kind's width variants beside it, and purges them.
// With no encoder configured it is the Go JPEG path that was there before.
// Returns the display width and size.
func (p *Pipeline) renderDisplay(ctx context.Context, key string, source []byte, cap int, meta map[string]string) (int, int, error) {
	newMeta := copyMeta(meta)
	newMeta[MetaNormalized] = "1"
	urls := []string{p.urlOf(key)}

	if p.opts.Encoder == nil || p.opts.DisplayFormat == "" {
		display, width, err := fitWidth(source, cap, p.opts.DisplayQuality)
		if err != nil {
			return 0, 0, fmt.Errorf("fit %s: %w", key, err)
		}
		// As before: the runner's format when the source was stored as is,
		// JPEG once it was resized.
		ct := contentTypeFor(p.opts.Format)
		if src, _, err := image.DecodeConfig(bytes.NewReader(source)); err == nil && width != src.Width {
			ct = "image/jpeg"
		}
		newMeta[MetaDisplayWidth] = strconv.Itoa(width)
		if err := p.store.Put(ctx, key, display, ct, newMeta); err != nil {
			return 0, 0, fmt.Errorf("put %s: %w", key, err)
		}
		p.purge(ctx, urls)
		return width, len(display), nil
	}

	format := p.opts.DisplayFormat
	display, err := p.opts.Encoder.Encode(ctx, source, cap, format, p.opts.DisplayQuality)
	if err != nil {
		return 0, 0, fmt.Errorf("encode %s: %w", key, err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(display))
	if err != nil {
		return 0, 0, fmt.Errorf("encode %s produced an undecodable image: %w", key, err)
	}
	width := cfg.Width

	// Variants first, so a key is never marked as having one it lacks.
	var written []string
	for _, w := range p.opts.Variants[KindOf(key)] {
		if w <= 0 || w >= width {
			continue
		}
		v, err := p.opts.Encoder.Encode(ctx, source, w, format, p.opts.DisplayQuality)
		if err != nil {
			return 0, 0, fmt.Errorf("encode %s%s: %w", key, VariantSuffix(w), err)
		}
		vk := key + VariantSuffix(w)
		vMeta := map[string]string{MetaDisplayWidth: strconv.Itoa(w), MetaDisplayFormat: format}
		if err := p.store.Put(ctx, vk, v, contentTypeFor(format), vMeta); err != nil {
			return 0, 0, fmt.Errorf("put %s: %w", vk, err)
		}
		written = append(written, strconv.Itoa(w))
		urls = append(urls, p.urlOf(vk))
	}

	newMeta[MetaDisplayWidth] = strconv.Itoa(width)
	newMeta[MetaDisplayFormat] = format
	newMeta[MetaDisplayVariants] = strings.Join(written, ",")
	if err := p.store.Put(ctx, key, display, contentTypeFor(format), newMeta); err != nil {
		return 0, 0, fmt.Errorf("put %s: %w", key, err)
	}
	p.purge(ctx, urls)
	return width, len(display), nil
}

func (p *Pipeline) urlOf(key string) string {
	return strings.TrimSuffix(p.opts.CDNBase, "/") + "/" + key
}

func (p *Pipeline) purge(ctx context.Context, urls []string) {
	if p.purger == nil || p.opts.CDNBase == "" {
		return
	}
	if err := p.purger.Purge(ctx, urls); err != nil {
		// The object is replaced; a stale edge copy ages out on its own.
		log.Printf("purge %v: %v", urls, err)
	}
}
