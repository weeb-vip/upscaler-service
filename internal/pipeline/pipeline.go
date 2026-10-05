// Package pipeline decides whether a stored image needs upscaling and, when
// it does, replaces it in the bucket: original kept beside it, metadata
// carried over, CDN purged.
package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/weeb-vip/upscaler-service/internal/bucket"
	"github.com/weeb-vip/upscaler-service/internal/purge"
)

// Upscaler is the one method of the Real-ESRGAN runner the pipeline uses.
type Upscaler interface {
	Bytes(ctx context.Context, image []byte, format string, scale int) ([]byte, error)
}

// Kind is what an object is, read off its key: the folder image-sync stores
// each type under. Root objects are the anime's own MyAnimeList image.
type Kind string

const (
	KindAnime     Kind = "anime"
	KindPoster    Kind = "poster"
	KindBanner    Kind = "banner"
	KindCharacter Kind = "character"
	KindStaff     Kind = "staff"
	KindWork      Kind = "work"
)

// KindOf reads the kind from a key such as weeb/posters/<id>. The prefix is
// everything before the last folder, so staging's weeb-staging/ works too.
func KindOf(key string) Kind {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	if len(parts) < 3 {
		return KindAnime
	}
	switch parts[len(parts)-2] {
	case "posters":
		return KindPoster
	case "banners":
		return KindBanner
	case "characters":
		return KindCharacter
	case "staff":
		return KindStaff
	case "works":
		return KindWork
	}
	return KindAnime
}

type Options struct {
	// MinWidth: an image at least this wide is left alone. 1000 means every
	// 225px MyAnimeList image and every 680px TheTVDB poster is upscaled, and
	// an object this pipeline already rewrote (2720px, 900px) is not touched
	// again -- which is what makes a re-run or a replayed event harmless.
	MinWidth int
	// KeepOriginal copies the object to <key><OrigSuffix> before replacing
	// it, unless that copy already exists.
	KeepOriginal bool
	OrigSuffix   string
	// Format the replacement is written in: jpg or webp. Not png: a 2720px
	// PNG is 6-12 MB for no visible gain, and the CDN resizes on delivery.
	Format string
	// CDNBase is the public origin keys are served from, for the purge.
	CDNBase string
	// Model names what did the work, recorded on the object.
	Model string
	// Scale per kind; 0 or absent means the runner's default. A 424px
	// MyAnimeList image at 2x lands where a 225px one did at 4x, with far
	// less invented detail; a 680px poster at 4x is 2720px.
	Scales map[Kind]int
	// DisplayWidths cap what is stored at the key, per kind; the full
	// result is kept at <key><FullSuffix> (default "-full"). Nil means
	// DefaultDisplayWidths; a kind set to 0 is not capped.
	DisplayWidths map[Kind]int
	// KeepFull stores the uncapped result beside the key.
	KeepFull   bool
	FullSuffix string
	// Quality of the capped JPEG.
	DisplayQuality int
}

// Metadata written on a replaced object. image-sync's `source-length` is
// carried over untouched: that is what keeps image-sync from re-downloading
// the original over this.
const (
	MetaUpscaled     = "upscaled"
	MetaFromWidth    = "upscaled-from-width"
	MetaUpscaledAt   = "upscaled-at"
	MetaDisplayWidth = "display-width"
)

type Pipeline struct {
	store  bucket.Store
	up     Upscaler
	purger purge.Purger
	opts   Options
}

func New(store bucket.Store, up Upscaler, purger purge.Purger, opts Options) *Pipeline {
	if opts.MinWidth == 0 {
		opts.MinWidth = 1000
	}
	if opts.OrigSuffix == "" {
		opts.OrigSuffix = "-orig"
	}
	if opts.Format == "" {
		opts.Format = "jpg"
	}
	if opts.DisplayWidths == nil {
		opts.DisplayWidths = DefaultDisplayWidths
	}
	if opts.FullSuffix == "" {
		opts.FullSuffix = "-full"
	}
	if opts.DisplayQuality == 0 {
		opts.DisplayQuality = 90
	}
	return &Pipeline{store: store, up: up, purger: purger, opts: opts}
}

// Outcome says what happened to one key.
type Outcome string

const (
	Upscaled        Outcome = "upscaled"
	Downsized       Outcome = "downsized"
	AlreadyWide     Outcome = "already-wide"
	AlreadyUpscaled Outcome = "already-upscaled"
	IsOriginal      Outcome = "is-original-copy"
	Undecodable     Outcome = "undecodable"
)

type Result struct {
	Key      string
	Outcome  Outcome
	Width    int
	NewWidth int
	Bytes    int
	Took     time.Duration
}

// Handle runs one key through the decision and, where it needs it, the
// upscale. Errors are for things worth retrying (bucket, binary); a key that
// merely does not need work is a Result, not an error.
func (p *Pipeline) Handle(ctx context.Context, key string) (Result, error) {
	start := time.Now()
	res := Result{Key: key}
	if strings.HasSuffix(key, p.opts.OrigSuffix) || strings.HasSuffix(key, p.opts.FullSuffix) {
		res.Outcome = IsOriginal
		return res, nil
	}

	data, _, meta, err := p.store.Get(ctx, key)
	if err != nil {
		return res, fmt.Errorf("get %s: %w", key, err)
	}
	// Provenance first, width second: a 225px image comes back at 900px,
	// which is still under MinWidth, and a replayed event would otherwise
	// upscale the upscale to 3600px.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		res.Outcome = Undecodable
		return res, nil
	}
	res.Width = cfg.Width
	cap := p.opts.DisplayWidths[KindOf(key)]
	if meta[MetaUpscaled] != "" {
		// Done before, but possibly before objects were capped: a 2720px
		// result sitting at the key is brought down to display size, with
		// the full result kept beside it first.
		if cap > 0 && cfg.Width > cap {
			return p.downsize(ctx, key, data, meta, cap, res)
		}
		res.Outcome = AlreadyUpscaled
		return res, nil
	}
	if cfg.Width >= p.opts.MinWidth {
		res.Outcome = AlreadyWide
		return res, nil
	}

	if p.opts.KeepOriginal {
		orig := key + p.opts.OrigSuffix
		exists, err := p.store.Exists(ctx, orig)
		if err != nil {
			return res, fmt.Errorf("stat %s: %w", orig, err)
		}
		if !exists {
			if err := p.store.Copy(ctx, key, orig); err != nil {
				return res, fmt.Errorf("keep original %s: %w", orig, err)
			}
		}
	}

	scale := p.opts.Scales[KindOf(key)]
	out, err := p.up.Bytes(ctx, data, p.opts.Format, scale)
	if err != nil {
		return res, fmt.Errorf("upscale %s: %w", key, err)
	}
	outCfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		return res, fmt.Errorf("upscale %s produced an undecodable image: %w", key, err)
	}

	newMeta := make(map[string]string, len(meta)+4)
	for k, v := range meta {
		newMeta[k] = v
	}
	newMeta[MetaUpscaled] = p.opts.Model
	if scale > 0 {
		newMeta[MetaUpscaled] = fmt.Sprintf("%s x%d", p.opts.Model, scale)
	}
	newMeta[MetaFromWidth] = strconv.Itoa(cfg.Width)
	newMeta[MetaUpscaledAt] = time.Now().UTC().Format(time.RFC3339)

	// The full result beside the key; the key itself gets the display size.
	if p.opts.KeepFull && cap > 0 && outCfg.Width > cap {
		if err := p.store.Put(ctx, key+p.opts.FullSuffix, out, contentTypeFor(p.opts.Format), newMeta); err != nil {
			return res, fmt.Errorf("put %s: %w", key+p.opts.FullSuffix, err)
		}
	}
	display, displayWidth, err := fitWidth(out, cap, p.opts.DisplayQuality)
	if err != nil {
		return res, fmt.Errorf("fit %s: %w", key, err)
	}
	ct := contentTypeFor(p.opts.Format)
	if displayWidth != outCfg.Width {
		ct = "image/jpeg"
	}
	newMeta[MetaDisplayWidth] = strconv.Itoa(displayWidth)
	if err := p.store.Put(ctx, key, display, ct, newMeta); err != nil {
		return res, fmt.Errorf("put %s: %w", key, err)
	}
	out = display
	outCfg.Width = displayWidth

	if p.purger != nil && p.opts.CDNBase != "" {
		url := strings.TrimSuffix(p.opts.CDNBase, "/") + "/" + key
		if err := p.purger.Purge(ctx, []string{url}); err != nil {
			// The object is replaced; a stale edge copy ages out on its own.
			log.Printf("purge %s: %v", url, err)
		}
	}

	res.Outcome = Upscaled
	res.NewWidth = outCfg.Width
	res.Bytes = len(out)
	res.Took = time.Since(start)
	return res, nil
}

// downsize brings an already-upscaled object at the key down to the display
// cap, keeping the full result at <key><FullSuffix> unless that exists.
func (p *Pipeline) downsize(ctx context.Context, key string, data []byte, meta map[string]string, cap int, res Result) (Result, error) {
	start := time.Now()
	if p.opts.KeepFull {
		full := key + p.opts.FullSuffix
		exists, err := p.store.Exists(ctx, full)
		if err != nil {
			return res, fmt.Errorf("stat %s: %w", full, err)
		}
		if !exists {
			if err := p.store.Copy(ctx, key, full); err != nil {
				return res, fmt.Errorf("keep full %s: %w", full, err)
			}
		}
	}
	display, displayWidth, err := fitWidth(data, cap, p.opts.DisplayQuality)
	if err != nil {
		return res, fmt.Errorf("fit %s: %w", key, err)
	}
	newMeta := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		newMeta[k] = v
	}
	newMeta[MetaDisplayWidth] = strconv.Itoa(displayWidth)
	if err := p.store.Put(ctx, key, display, "image/jpeg", newMeta); err != nil {
		return res, fmt.Errorf("put %s: %w", key, err)
	}
	if p.purger != nil && p.opts.CDNBase != "" {
		url := strings.TrimSuffix(p.opts.CDNBase, "/") + "/" + key
		if err := p.purger.Purge(ctx, []string{url}); err != nil {
			log.Printf("purge %s: %v", url, err)
		}
	}
	res.Outcome = Downsized
	res.NewWidth = displayWidth
	res.Bytes = len(display)
	res.Took = time.Since(start)
	return res, nil
}

func contentTypeFor(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
