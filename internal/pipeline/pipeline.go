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
	// MaxBytes: an object within its width cap but heavier than this is
	// re-encoded as JPEG at DisplayQuality. Nothing resizes on delivery any
	// more, so a 1.3 MB poster is 1.3 MB on every card that shows it. 0
	// turns it off.
	MaxBytes int
	// Encoder writes the display copies (WebP by default) and the width
	// variants beside the key; nil keeps the Go JPEG path, for tests.
	Encoder Encoder
	// DisplayFormat is what the key holds: "webp" (default) or "jpg". An
	// object whose recorded display-format differs is re-encoded from its
	// best source on the next walk.
	DisplayFormat string
	// Variants per kind: the stored widths beside the key (<key>-w320).
	// Nil means DefaultVariants.
	Variants map[Kind][]int
	// SkipUpscale: no model. A source that would be upscaled is only given
	// the display treatment (re-encoded if heavy) and left for a later
	// walk, which still finds it unmarked and small. The fast first pass
	// over a bucket: every oversized object is brought down in minutes,
	// the slow work comes after.
	SkipUpscale bool
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
		opts.DisplayQuality = 85
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 250 * 1024
	}
	if opts.Variants == nil {
		opts.Variants = DefaultVariants
	}
	return &Pipeline{store: store, up: up, purger: purger, opts: opts}
}

// Outcome says what happened to one key.
type Outcome string

const (
	Upscaled        Outcome = "upscaled"
	Downsized       Outcome = "downsized"
	Recompressed    Outcome = "recompressed"
	AlreadyWide     Outcome = "already-wide"
	AlreadyUpscaled Outcome = "already-upscaled"
	IsOriginal      Outcome = "is-original-copy"
	Undecodable     Outcome = "undecodable"
	// Repaired: an earlier result at the key was all black, the original
	// beside it was not, so the original went back and was upscaled again.
	Repaired Outcome = "repaired"
	// Blank: an all-black object with no usable original to restore from.
	Blank Outcome = "blank"
	// Reencoded: an already-treated object whose display copy was in another
	// format; re-made from its best source, variants beside it.
	Reencoded Outcome = "re-encoded"
)

// MetaNormalized marks an object the pipeline has sized and compressed for
// display, so a re-run leaves it alone.
const MetaNormalized = "normalized"

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
	repaired := false
	if meta[MetaUpscaled] != "" && isBlank(data) {
		// The ncnn Vulkan builds wrote an all-black result when the device
		// ran out of memory, and marked it done. The original beside the
		// key is what the model should have seen; put it back and go again.
		orig, origMeta, ok, err := p.usableOriginal(ctx, key)
		if err != nil {
			return res, err
		}
		if !ok {
			res.Outcome = Blank
			return res, nil
		}
		if err := p.store.Put(ctx, key, orig, contentTypeOf(orig), origMeta); err != nil {
			return res, fmt.Errorf("restore %s: %w", key, err)
		}
		log.Printf("black image found at %s: reverted to %s%s, upscaling it again", key, key, p.opts.OrigSuffix)
		data, meta = orig, origMeta
		if cfg, _, err = image.DecodeConfig(bytes.NewReader(data)); err != nil {
			res.Outcome = Undecodable
			return res, nil
		}
		res.Width = cfg.Width
		repaired = true
	}
	// A treated object in the old display format: re-encode it from its
	// best source (the uncapped upscale, else the original), variants and
	// all. This is the walk that turns a bucket of JPEG into WebP.
	if (meta[MetaUpscaled] != "" || meta[MetaNormalized] != "") && p.displayStale(meta) {
		source, err := p.bestSource(ctx, key, data)
		if err != nil {
			return res, err
		}
		width, size, err := p.renderDisplay(ctx, key, source, cap, meta)
		if err != nil {
			return res, err
		}
		res.Outcome = Reencoded
		res.NewWidth = width
		res.Bytes = size
		res.Took = time.Since(start)
		return res, nil
	}
	if meta[MetaUpscaled] != "" {
		// Done before, but possibly before objects were capped: a 2720px
		// result sitting at the key is brought down to display size, with
		// the full result kept beside it first.
		if cap > 0 && cfg.Width > cap {
			return p.downsize(ctx, key, data, meta, cap, res, true)
		}
		res.Outcome = AlreadyUpscaled
		return res, nil
	}
	// Nothing to gain from the model once the source is already at the
	// width the key is capped to: a 600px staff photo upscaled to 1200px and
	// capped back to 600px was 26 seconds for the same pixels. Such an
	// object only gets the display treatment below.
	atCap := cap > 0 && cfg.Width >= cap
	if cfg.Width >= p.opts.MinWidth || atCap || p.opts.SkipUpscale {
		// Not upscaled, but still served as stored: a 1920px banner or a
		// heavy poster gets the same display treatment, once.
		if meta[MetaNormalized] == "" && cap > 0 && cfg.Width > cap {
			return p.downsize(ctx, key, data, meta, cap, res, false)
		}
		if meta[MetaNormalized] == "" && p.opts.MaxBytes > 0 && len(data) > p.opts.MaxBytes {
			return p.recompress(ctx, key, data, meta, res)
		}
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
	if isBlank(out) && !isBlank(data) {
		// Never let a black frame replace a picture: an error here keeps
		// the key as it is and sends the event round the retry stream.
		return res, fmt.Errorf("black image produced for %s: not stored, the key is unchanged", key)
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
	displayWidth, displaySize, err := p.renderDisplay(ctx, key, out, cap, newMeta)
	if err != nil {
		return res, err
	}

	res.Outcome = Upscaled
	if repaired {
		res.Outcome = Repaired
	}
	res.NewWidth = displayWidth
	res.Bytes = displaySize
	res.Took = time.Since(start)
	return res, nil
}

// usableOriginal fetches <key><OrigSuffix> when it exists and is a picture
// rather than another black frame, with the pipeline's own provenance
// stripped from its metadata so the key reads as untouched again.
func (p *Pipeline) usableOriginal(ctx context.Context, key string) ([]byte, map[string]string, bool, error) {
	orig := key + p.opts.OrigSuffix
	exists, err := p.store.Exists(ctx, orig)
	if err != nil {
		return nil, nil, false, fmt.Errorf("stat %s: %w", orig, err)
	}
	if !exists {
		return nil, nil, false, nil
	}
	data, _, meta, err := p.store.Get(ctx, orig)
	if err != nil {
		return nil, nil, false, fmt.Errorf("get %s: %w", orig, err)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || isBlank(data) {
		return nil, nil, false, nil
	}
	clean := copyMeta(meta)
	for _, k := range []string{MetaUpscaled, MetaFromWidth, MetaUpscaledAt, MetaDisplayWidth, MetaNormalized, MetaDisplayFormat, MetaDisplayVariants} {
		delete(clean, k)
	}
	return data, clean, true, nil
}

// downsize brings an object at the key down to the display cap. For an
// upscaled result the uncapped copy is kept at <key><FullSuffix>; for an
// untouched original it is kept at <key><OrigSuffix>, unless either exists.
func (p *Pipeline) downsize(ctx context.Context, key string, data []byte, meta map[string]string, cap int, res Result, upscaled bool) (Result, error) {
	start := time.Now()
	keep, suffix := p.opts.KeepFull, p.opts.FullSuffix
	if !upscaled {
		keep, suffix = p.opts.KeepOriginal, p.opts.OrigSuffix
	}
	if keep {
		if err := p.keepCopy(ctx, key, key+suffix); err != nil {
			return res, err
		}
	}
	displayWidth, displaySize, err := p.renderDisplay(ctx, key, data, cap, meta)
	if err != nil {
		return res, err
	}
	res.Outcome = Downsized
	res.NewWidth = displayWidth
	res.Bytes = displaySize
	res.Took = time.Since(start)
	return res, nil
}

// recompress re-encodes a heavy object within its width cap as JPEG at
// DisplayQuality, original kept at <key><OrigSuffix>. A result no smaller
// than the input is not written; the object is marked normalized either
// way so it is not revisited.
func (p *Pipeline) recompress(ctx context.Context, key string, data []byte, meta map[string]string, res Result) (Result, error) {
	start := time.Now()
	if p.opts.Encoder != nil && p.opts.DisplayFormat != "" {
		if p.opts.KeepOriginal {
			if err := p.keepCopy(ctx, key, key+p.opts.OrigSuffix); err != nil {
				return res, err
			}
		}
		width, size, err := p.renderDisplay(ctx, key, data, 0, meta)
		if err != nil {
			return res, err
		}
		res.Outcome = Recompressed
		res.NewWidth = width
		res.Bytes = size
		res.Took = time.Since(start)
		return res, nil
	}
	display, width, err := reencode(data, p.opts.DisplayQuality)
	if err != nil {
		return res, fmt.Errorf("re-encode %s: %w", key, err)
	}
	if len(display) >= len(data) {
		newMeta := copyMeta(meta)
		newMeta[MetaNormalized] = "1"
		if err := p.store.Put(ctx, key, data, contentTypeOf(data), newMeta); err != nil {
			return res, fmt.Errorf("put %s: %w", key, err)
		}
		res.Outcome = AlreadyWide
		return res, nil
	}
	if p.opts.KeepOriginal {
		if err := p.keepCopy(ctx, key, key+p.opts.OrigSuffix); err != nil {
			return res, err
		}
	}
	if err := p.putDisplay(ctx, key, display, meta, width); err != nil {
		return res, err
	}
	res.Outcome = Recompressed
	res.NewWidth = width
	res.Bytes = len(display)
	res.Took = time.Since(start)
	return res, nil
}

func (p *Pipeline) keepCopy(ctx context.Context, key, copyKey string) error {
	exists, err := p.store.Exists(ctx, copyKey)
	if err != nil {
		return fmt.Errorf("stat %s: %w", copyKey, err)
	}
	if exists {
		return nil
	}
	if err := p.store.Copy(ctx, key, copyKey); err != nil {
		return fmt.Errorf("keep %s: %w", copyKey, err)
	}
	return nil
}

func (p *Pipeline) putDisplay(ctx context.Context, key string, display []byte, meta map[string]string, width int) error {
	newMeta := copyMeta(meta)
	newMeta[MetaDisplayWidth] = strconv.Itoa(width)
	newMeta[MetaNormalized] = "1"
	if err := p.store.Put(ctx, key, display, "image/jpeg", newMeta); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	if p.purger != nil && p.opts.CDNBase != "" {
		url := strings.TrimSuffix(p.opts.CDNBase, "/") + "/" + key
		if err := p.purger.Purge(ctx, []string{url}); err != nil {
			log.Printf("purge %s: %v", url, err)
		}
	}
	return nil
}

func copyMeta(meta map[string]string) map[string]string {
	out := make(map[string]string, len(meta)+2)
	for k, v := range meta {
		out[k] = v
	}
	return out
}

func contentTypeOf(data []byte) string {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "application/octet-stream"
	}
	switch format {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
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
