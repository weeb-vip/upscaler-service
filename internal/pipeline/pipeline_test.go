package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/weeb-vip/upscaler-service/internal/bucket"
)

type obj struct {
	data []byte
	ct   string
	meta map[string]string
}

type memStore struct{ objs map[string]obj }

func (m *memStore) Get(_ context.Context, key string) ([]byte, string, map[string]string, error) {
	o, ok := m.objs[key]
	if !ok {
		return nil, "", nil, context.Canceled
	}
	return o.data, o.ct, o.meta, nil
}
func (m *memStore) Put(_ context.Context, key string, data []byte, ct string, meta map[string]string) error {
	m.objs[key] = obj{data, ct, meta}
	return nil
}
func (m *memStore) Copy(_ context.Context, src, dst string) error {
	m.objs[dst] = m.objs[src]
	return nil
}
func (m *memStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.objs[key]
	return ok, nil
}
func (m *memStore) List(context.Context, string) <-chan bucket.Entry {
	ch := make(chan bucket.Entry)
	close(ch)
	return ch
}

type fakeUp struct {
	calls int
	scale int
}

// Produces a PNG `scale` times as wide as asked (4 when unset), whatever
// the format name, and remembers the scale it was asked for.
func (f *fakeUp) Bytes(_ context.Context, in []byte, _ string, scale int) ([]byte, error) {
	f.calls++
	f.scale = scale
	if scale == 0 {
		scale = 4
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(in))
	var buf bytes.Buffer
	png.Encode(&buf, lit(cfg.Width*scale, cfg.Height*scale))
	return buf.Bytes(), nil
}

// blackUp is a runner that writes black frames, as the ncnn Vulkan build
// did when the device could not allocate memory.
type blackUp struct{ calls int }

func (f *blackUp) Bytes(_ context.Context, in []byte, _ string, scale int) ([]byte, error) {
	f.calls++
	if scale == 0 {
		scale = 4
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(in))
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, cfg.Width*scale, cfg.Height*scale)))
	return buf.Bytes(), nil
}

type fakePurge struct{ urls []string }

func (f *fakePurge) Purge(_ context.Context, urls []string) error {
	f.urls = append(f.urls, urls...)
	return nil
}

// lit is a picture rather than a black frame: a mid-grey field with a
// bright stripe, so the blank check sees something.
func lit(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 120, 80, 160, 255
	}
	return img
}

func jpegOf(w, h int) []byte {
	var buf bytes.Buffer
	jpeg.Encode(&buf, lit(w, h), nil)
	return buf.Bytes()
}

func blackJpegOf(w, h int) []byte {
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil)
	return buf.Bytes()
}

func setup(width int) (*memStore, *fakeUp, *fakePurge, *Pipeline) {
	store := &memStore{objs: map[string]obj{
		"weeb/posters/one": {jpegOf(width, width*3/2), "image/jpeg", map[string]string{"source-length": "12345"}},
	}}
	up := &fakeUp{}
	pg := &fakePurge{}
	p := New(store, up, pg, Options{KeepOriginal: true, KeepFull: true, CDNBase: "https://cdn.weeb.vip", Model: "realesrgan-x4plus-anime"})
	return store, up, pg, p
}

func TestANarrowImageIsUpscaledInPlaceWithTheOriginalKeptBesideIt(t *testing.T) {
	store, up, pg, p := setup(225)

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil {
		t.Fatal(err)
	}
	// 225 -> 900 at 4x, which is under the 1000px poster cap: stored as is.
	if res.Outcome != Upscaled || res.Width != 225 || res.NewWidth != 900 {
		t.Fatalf("result %+v", res)
	}
	if _, full := store.objs["weeb/posters/one-full"]; full {
		t.Error("no -full copy is needed when the result is already within the cap")
	}
	if up.calls != 1 {
		t.Errorf("upscaler called %d times", up.calls)
	}
	orig, ok := store.objs["weeb/posters/one-orig"]
	if !ok || len(orig.data) != len(jpegOf(225, 337)) {
		t.Error("the original was not kept at <key>-orig")
	}
	replaced := store.objs["weeb/posters/one"]
	if replaced.ct != "image/jpeg" {
		t.Errorf("content type %q", replaced.ct)
	}
	// image-sync's skip rule depends on this surviving the rewrite.
	if replaced.meta["source-length"] != "12345" {
		t.Errorf("source-length not carried over: %v", replaced.meta)
	}
	if replaced.meta[MetaUpscaled] != "realesrgan-x4plus-anime" || replaced.meta[MetaFromWidth] != "225" {
		t.Errorf("provenance missing: %v", replaced.meta)
	}
	if len(pg.urls) != 1 || pg.urls[0] != "https://cdn.weeb.vip/weeb/posters/one" {
		t.Errorf("purged %v", pg.urls)
	}
}

func TestAWideImageIsLeftAlone(t *testing.T) {
	// 1000px: at the poster cap, not over it, and light enough to keep.
	store, up, _, p := setup(1000)
	before := store.objs["weeb/posters/one"]

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != AlreadyWide || up.calls != 0 {
		t.Fatalf("result %+v, calls %d", res, up.calls)
	}
	if _, kept := store.objs["weeb/posters/one-orig"]; kept {
		t.Error("no original copy should be made for an object that was not touched")
	}
	if len(store.objs["weeb/posters/one"].data) != len(before.data) {
		t.Error("object changed")
	}
}

// Replayed events and re-runs: the second pass sees the 900px result and
// stops, and the -orig copy from the first pass is not overwritten.
func TestARerunIsHarmless(t *testing.T) {
	store, up, _, p := setup(225)
	p.Handle(context.Background(), "weeb/posters/one")
	origAfterFirst := store.objs["weeb/posters/one-orig"]

	res, _ := p.Handle(context.Background(), "weeb/posters/one")

	// 900px is still under MinWidth; the provenance metadata is what stops it.
	if res.Outcome != AlreadyUpscaled || up.calls != 1 {
		t.Fatalf("second pass %+v, calls %d", res, up.calls)
	}
	if len(store.objs["weeb/posters/one-orig"].data) != len(origAfterFirst.data) {
		t.Error("the original copy was replaced")
	}
}

func TestTheOriginalCopyItselfIsNeverUpscaled(t *testing.T) {
	_, up, _, p := setup(225)
	res, err := p.Handle(context.Background(), "weeb/posters/one-orig")
	if err != nil || res.Outcome != IsOriginal || up.calls != 0 {
		t.Fatalf("%+v %v calls=%d", res, err, up.calls)
	}
}

func TestSomethingThatIsNotAnImageIsReportedNotRetried(t *testing.T) {
	store, up, _, p := setup(225)
	store.objs["weeb/posters/one"] = obj{[]byte("<html>not found</html>"), "text/html", nil}

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil || res.Outcome != Undecodable || up.calls != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestKindIsReadOffTheKey(t *testing.T) {
	cases := map[string]Kind{
		"weeb/abc":                 KindAnime,
		"weeb-staging/abc":         KindAnime,
		"weeb/posters/abc":         KindPoster,
		"weeb-staging/banners/abc": KindBanner,
		"weeb/characters/abc":      KindCharacter,
		"weeb/staff/abc":           KindStaff,
		"weeb/works/abc":           KindWork,
		"weeb/somethingelse/abc":   KindAnime,
	}
	for key, want := range cases {
		if got := KindOf(key); got != want {
			t.Errorf("%s -> %s, want %s", key, got, want)
		}
	}
}

func TestScaleFollowsTheKind(t *testing.T) {
	store := &memStore{objs: map[string]obj{
		"weeb/one":         {jpegOf(424, 600), "image/jpeg", nil},
		"weeb/posters/one": {jpegOf(680, 1000), "image/jpeg", nil},
	}}
	up := &fakeUp{}
	p := New(store, up, nil, Options{Model: "m", Scales: map[Kind]int{KindAnime: 2}})

	res, _ := p.Handle(context.Background(), "weeb/one")
	// 2x of 424 is 848, then capped to the 600px display width for roots.
	if up.scale != 2 || res.NewWidth != 600 {
		t.Errorf("root image: asked scale %d, got %dpx", up.scale, res.NewWidth)
	}
	if got := store.objs["weeb/one"].meta[MetaUpscaled]; got != "m x2" {
		t.Errorf("provenance should name the scale, got %q", got)
	}

	res, _ = p.Handle(context.Background(), "weeb/posters/one")
	// The runner's default scale, then the 1000px poster cap.
	if up.scale != 0 || res.NewWidth != 1000 {
		t.Errorf("poster: asked scale %d, got %dpx (default is the runner's)", up.scale, res.NewWidth)
	}
}

func widthOf(data []byte) int {
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
	return cfg.Width
}

// What the CDN serves is the object at the key, as is, so it must be a
// display size; the full result lives beside it.
func TestTheKeyGetsADisplaySizeAndTheFullResultSitsBesideIt(t *testing.T) {
	store, up, _, p := setup(680) // a TheTVDB poster: 4x is 2720px

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Upscaled || res.NewWidth != 1000 {
		t.Fatalf("result %+v", res)
	}
	if w := widthOf(store.objs["weeb/posters/one"].data); w != 1000 {
		t.Errorf("object at the key is %dpx wide, want the 1000px cap", w)
	}
	if store.objs["weeb/posters/one"].ct != "image/jpeg" {
		t.Errorf("capped object should be JPEG, got %s", store.objs["weeb/posters/one"].ct)
	}
	if store.objs["weeb/posters/one"].meta[MetaDisplayWidth] != "1000" {
		t.Errorf("display-width meta: %v", store.objs["weeb/posters/one"].meta)
	}
	if w := widthOf(store.objs["weeb/posters/one-full"].data); w != 2720 {
		t.Errorf("full result at -full is %dpx, want 2720", w)
	}
	if _, kept := store.objs["weeb/posters/one-orig"]; !kept {
		t.Error("the original should still be kept")
	}
	if up.calls != 1 {
		t.Errorf("upscaler called %d times", up.calls)
	}
}

// Objects rewritten before the cap existed: 2720px sitting at the key.
func TestAnOversizedEarlierResultIsBroughtDownWithoutUpscalingAgain(t *testing.T) {
	store, up, pg, p := setup(2720)
	store.objs["weeb/posters/one"] = obj{jpegOf(2720, 4000), "image/jpeg", map[string]string{"source-length": "12345", MetaUpscaled: "realesrgan-x4plus-anime"}}

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Downsized || res.NewWidth != 1000 || up.calls != 0 {
		t.Fatalf("result %+v, upscaler calls %d", res, up.calls)
	}
	if w := widthOf(store.objs["weeb/posters/one"].data); w != 1000 {
		t.Errorf("key is %dpx, want 1000", w)
	}
	if w := widthOf(store.objs["weeb/posters/one-full"].data); w != 2720 {
		t.Errorf("the full result should have been kept at -full first, got %dpx", w)
	}
	if store.objs["weeb/posters/one"].meta[MetaUpscaled] == "" {
		t.Error("provenance lost")
	}
	if len(pg.urls) != 1 {
		t.Errorf("purged %v", pg.urls)
	}

	// And a second pass leaves it alone.
	res, _ = p.Handle(context.Background(), "weeb/posters/one")
	if res.Outcome != AlreadyUpscaled {
		t.Errorf("second pass %+v", res)
	}
}

func TestTheFullCopyIsNeverProcessedItself(t *testing.T) {
	_, up, _, p := setup(225)
	res, err := p.Handle(context.Background(), "weeb/posters/one-full")
	if err != nil || res.Outcome != IsOriginal || up.calls != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestEachKindHasItsOwnCap(t *testing.T) {
	store := &memStore{objs: map[string]obj{
		"weeb/one":         {jpegOf(424, 600), "image/jpeg", nil},
		"weeb/banners/one": {jpegOf(680, 1000), "image/jpeg", nil},
	}}
	p := New(store, &fakeUp{}, nil, Options{Scales: map[Kind]int{KindAnime: 2}})

	res, _ := p.Handle(context.Background(), "weeb/one")
	if res.NewWidth != 600 || widthOf(store.objs["weeb/one"].data) != 600 {
		t.Errorf("root image: %+v (2x of 424 is 848, capped at 600)", res)
	}
	res, _ = p.Handle(context.Background(), "weeb/banners/one")
	if res.NewWidth != 1920 || widthOf(store.objs["weeb/banners/one"].data) != 1920 {
		t.Errorf("banner: %+v (4x of 680 is 2720, capped at 1920)", res)
	}
}

// Nothing resizes on delivery any more, so objects that never needed an
// upscale still need to be a display size and weight: a 1920px banner is
// fine as it is, a 3000px scan is brought down, a 1.3 MB poster re-encoded.
func TestUntouchedObjectsAreNormalisedForDisplayOnce(t *testing.T) {
	var noisy bytes.Buffer
	// Random pixels do not compress: a 1000px-wide image (at the cap, so
	// not an upscale candidate) well over 250 KB.
	rnd := image.NewRGBA(image.Rect(0, 0, 1000, 1470))
	for i := range rnd.Pix {
		rnd.Pix[i] = uint8((i * 7919) % 251)
	}
	jpeg.Encode(&noisy, rnd, &jpeg.Options{Quality: 100})
	store := &memStore{objs: map[string]obj{
		"weeb/posters/heavy": {noisy.Bytes(), "image/jpeg", map[string]string{"source-length": "1"}},
		"weeb/posters/wide":  {jpegOf(3000, 4400), "image/jpeg", nil},
		"weeb/banners/fine":  {jpegOf(1920, 1080), "image/jpeg", nil},
	}}
	up := &fakeUp{}
	p := New(store, up, nil, Options{KeepOriginal: true, MaxBytes: 250 * 1024})

	res, err := p.Handle(context.Background(), "weeb/posters/heavy")
	if err != nil || res.Outcome != Recompressed {
		t.Fatalf("heavy: %+v %v", res, err)
	}
	if len(store.objs["weeb/posters/heavy"].data) >= noisy.Len() {
		t.Error("heavy poster did not get smaller")
	}
	if _, kept := store.objs["weeb/posters/heavy-orig"]; !kept {
		t.Error("original of the heavy poster not kept")
	}
	if store.objs["weeb/posters/heavy"].meta["source-length"] != "1" {
		t.Error("metadata lost on recompress")
	}

	res, err = p.Handle(context.Background(), "weeb/posters/wide")
	if err != nil || res.Outcome != Downsized || res.NewWidth != 1000 {
		t.Fatalf("wide: %+v %v", res, err)
	}
	if _, kept := store.objs["weeb/posters/wide-orig"]; !kept {
		t.Error("original of the wide poster not kept")
	}

	res, err = p.Handle(context.Background(), "weeb/banners/fine")
	if err != nil || res.Outcome != AlreadyWide {
		t.Fatalf("fine: %+v %v", res, err)
	}
	if up.calls != 0 {
		t.Errorf("no upscaling expected, got %d calls", up.calls)
	}

	// Second pass: all marked normalised, nothing happens.
	for _, k := range []string{"weeb/posters/heavy", "weeb/posters/wide"} {
		res, _ = p.Handle(context.Background(), k)
		if res.Outcome != AlreadyWide {
			t.Errorf("%s second pass: %+v", k, res)
		}
	}
}

// A source already at its kind's display width gains nothing from the
// model: 600px staff photo in, 600px out, 26 seconds of CPU for nothing.
func TestASourceAtItsDisplayCapIsNotUpscaled(t *testing.T) {
	store := &memStore{objs: map[string]obj{
		"weeb/staff/one": {jpegOf(600, 800), "image/jpeg", nil},
		"weeb/staff/two": {jpegOf(386, 500), "image/jpeg", nil},
	}}
	up := &fakeUp{}
	p := New(store, up, nil, Options{})

	res, _ := p.Handle(context.Background(), "weeb/staff/one")
	if res.Outcome != AlreadyWide || up.calls != 0 {
		t.Errorf("600px staff at the 600px cap: %+v, upscaler calls %d", res, up.calls)
	}
	res, _ = p.Handle(context.Background(), "weeb/staff/two")
	if res.Outcome != Upscaled || res.NewWidth != 600 || up.calls != 1 {
		t.Errorf("386px staff below the cap: %+v, upscaler calls %d", res, up.calls)
	}
}

// The lavapipe deployments (1.5.0-1.7.0) wrote all-black results and
// marked them done, with the untouched original beside the key. A walk
// over the bucket puts the original back and upscales it again.
func TestABlackEarlierResultIsRestoredFromTheOriginalAndUpscaledAgain(t *testing.T) {
	store, up, pg, p := setup(424)
	done := map[string]string{"source-length": "12345", MetaUpscaled: "realesr-general-x4v3 x2", MetaFromWidth: "424", MetaNormalized: "1", MetaDisplayWidth: "600"}
	store.objs["weeb/one"] = obj{blackJpegOf(600, 849), "image/jpeg", done}
	store.objs["weeb/one-full"] = obj{blackJpegOf(848, 1200), "image/jpeg", done}
	store.objs["weeb/one-orig"] = obj{jpegOf(424, 600), "image/jpeg", map[string]string{"source-length": "12345"}}

	res, err := p.Handle(context.Background(), "weeb/one")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Repaired || res.Width != 424 || res.NewWidth != 600 || up.calls != 1 {
		t.Fatalf("result %+v, upscaler calls %d", res, up.calls)
	}
	// upscaled at the fake's 4x and capped to 600; the full result replaced the black one too
	for _, k := range []string{"weeb/one", "weeb/one-full"} {
		if isBlank(store.objs[k].data) {
			t.Errorf("%s is still black", k)
		}
	}
	if w := widthOf(store.objs["weeb/one-full"].data); w != 1696 {
		t.Errorf("full result %dpx", w)
	}
	got := store.objs["weeb/one"].meta
	if got[MetaUpscaled] != "realesrgan-x4plus-anime" || got[MetaFromWidth] != "424" || got["source-length"] != "12345" {
		t.Errorf("metadata after repair: %v", got)
	}
	if len(store.objs["weeb/one-orig"].data) != len(jpegOf(424, 600)) {
		t.Error("the original copy was touched")
	}
	if len(pg.urls) != 1 {
		t.Errorf("purged %v", pg.urls)
	}
	// And it is done: a second pass sees a lit, marked result.
	res, _ = p.Handle(context.Background(), "weeb/one")
	if res.Outcome != AlreadyUpscaled || up.calls != 1 {
		t.Errorf("second pass %+v, calls %d", res, up.calls)
	}
}

func TestABlackResultWithNoUsableOriginalIsReportedAndLeftAlone(t *testing.T) {
	store, up, _, p := setup(424)
	done := map[string]string{MetaUpscaled: "realesr-general-x4v3 x2"}
	store.objs["weeb/none"] = obj{blackJpegOf(600, 849), "image/jpeg", done}
	store.objs["weeb/both"] = obj{blackJpegOf(600, 849), "image/jpeg", done}
	store.objs["weeb/both-orig"] = obj{blackJpegOf(424, 600), "image/jpeg", nil}

	for _, k := range []string{"weeb/none", "weeb/both"} {
		res, err := p.Handle(context.Background(), k)
		if err != nil || res.Outcome != Blank {
			t.Errorf("%s: %+v %v", k, res, err)
		}
		if !isBlank(store.objs[k].data) {
			t.Errorf("%s was rewritten", k)
		}
	}
	if up.calls != 0 {
		t.Errorf("upscaler called %d times", up.calls)
	}
}

// A runner that answers a picture with a black frame is a failed run, not
// a result: the key keeps its original and the error goes round the retry.
func TestABlackUpscaleIsRejectedAndTheKeyKeptAsItWas(t *testing.T) {
	store, _, pg, _ := setup(225)
	up := &blackUp{}
	p := New(store, up, pg, Options{KeepOriginal: true, KeepFull: true, CDNBase: "https://cdn.weeb.vip"})
	before := store.objs["weeb/posters/one"]

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err == nil || res.Outcome == Upscaled {
		t.Fatalf("expected an error, got %+v %v", res, err)
	}
	after := store.objs["weeb/posters/one"]
	if len(after.data) != len(before.data) || after.meta[MetaUpscaled] != "" {
		t.Error("the key was replaced with the black result")
	}
	if _, full := store.objs["weeb/posters/one-full"]; full {
		t.Error("a black full result was stored")
	}
	if len(pg.urls) != 0 {
		t.Errorf("purged %v", pg.urls)
	}
}

func TestBlankTellsABlackFrameFromADarkPicture(t *testing.T) {
	if !isBlank(blackJpegOf(600, 849)) {
		t.Error("black JPEG not seen as blank")
	}
	if isBlank(jpegOf(600, 849)) {
		t.Error("a lit picture seen as blank")
	}
	// Dark with one highlight: still a picture.
	dark := image.NewRGBA(image.Rect(0, 0, 600, 849))
	for i := 0; i < len(dark.Pix); i += 4 {
		dark.Pix[i+3] = 255
	}
	for y := 400; y < 440; y++ {
		for x := 280; x < 320; x++ {
			o := dark.PixOffset(x, y)
			dark.Pix[o], dark.Pix[o+1], dark.Pix[o+2] = 200, 40, 60
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, dark, nil)
	if isBlank(buf.Bytes()) {
		t.Error("a dark picture with a highlight seen as blank")
	}
	if isBlank([]byte("not an image")) {
		t.Error("undecodable data must not count as blank")
	}
}

// restore: the quick pass that needs no model. Black keys get their
// original back; lit keys are left alone unless asked for every key.
func TestRestorePutsTheOriginalBackOverABlackKeyOnly(t *testing.T) {
	store, up, pg, p := setup(424)
	done := map[string]string{"source-length": "1", MetaUpscaled: "x", MetaNormalized: "1", MetaDisplayWidth: "600"}
	store.objs["weeb/black"] = obj{blackJpegOf(600, 849), "image/jpeg", done}
	store.objs["weeb/black-orig"] = obj{jpegOf(424, 600), "image/jpeg", map[string]string{"source-length": "1"}}
	store.objs["weeb/good"] = obj{jpegOf(600, 849), "image/jpeg", done}
	store.objs["weeb/good-orig"] = obj{jpegOf(424, 600), "image/jpeg", nil}
	store.objs["weeb/lost"] = obj{blackJpegOf(600, 849), "image/jpeg", done}
	store.objs["weeb/lost-orig"] = obj{blackJpegOf(424, 600), "image/jpeg", nil}

	res, err := p.Restore(context.Background(), "weeb/black", false)
	if err != nil || res.Outcome != Restored || res.Width != 600 || res.NewWidth != 424 {
		t.Fatalf("black: %+v %v", res, err)
	}
	got := store.objs["weeb/black"]
	if isBlank(got.data) || widthOf(got.data) != 424 || got.meta[MetaUpscaled] != "" || got.meta[MetaNormalized] != "" || got.meta["source-length"] != "1" {
		t.Errorf("after restore: %dpx meta %v", widthOf(got.data), got.meta)
	}
	if len(pg.urls) != 1 || pg.urls[0] != "https://cdn.weeb.vip/weeb/black" {
		t.Errorf("purged %v", pg.urls)
	}
	// And the walk then upscales it like any untouched source.
	hres, err := p.Handle(context.Background(), "weeb/black")
	if err != nil || hres.Outcome != Upscaled || up.calls != 1 {
		t.Errorf("walk after restore: %+v %v calls=%d", hres, err, up.calls)
	}

	res, _ = p.Restore(context.Background(), "weeb/good", false)
	if res.Outcome != KeyIsFine || widthOf(store.objs["weeb/good"].data) != 600 {
		t.Errorf("good key touched: %+v", res)
	}
	res, _ = p.Restore(context.Background(), "weeb/good", true)
	if res.Outcome != Restored || widthOf(store.objs["weeb/good"].data) != 424 {
		t.Errorf("--all should restore a lit key too: %+v", res)
	}
	res, _ = p.Restore(context.Background(), "weeb/lost", false)
	if res.Outcome != NoOriginal || !isBlank(store.objs["weeb/lost"].data) {
		t.Errorf("a black original is no use: %+v", res)
	}
	if res, _ := p.Restore(context.Background(), "weeb/black-orig", false); res.Outcome != IsOriginal {
		t.Errorf("an -orig key itself: %+v", res)
	}
}

// A fake display encoder: a PNG of the asked width (never wider than the
// source), remembering what it was asked for.
type fakeEnc struct{ calls []string }

func (f *fakeEnc) Encode(_ context.Context, src []byte, width int, format string, quality int) ([]byte, error) {
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(src))
	w := cfg.Width
	if width > 0 && width < w {
		w = width
	}
	h := cfg.Height * w / cfg.Width
	f.calls = append(f.calls, fmt.Sprintf("%dx%s", w, format))
	var buf bytes.Buffer
	png.Encode(&buf, lit(w, h))
	return buf.Bytes(), nil
}

func webpPipeline(store *memStore, up Upscaler) (*fakeEnc, *fakePurge, *Pipeline) {
	enc := &fakeEnc{}
	pg := &fakePurge{}
	p := New(store, up, pg, Options{KeepOriginal: true, KeepFull: true, CDNBase: "https://cdn.weeb.vip", Model: "m", Encoder: enc, DisplayFormat: "webp"})
	return enc, pg, p
}

// The display copy is WebP at the cap, the kind's variants sit beside it,
// and the key says so.
func TestTheDisplayCopyIsWebPWithVariantsBesideIt(t *testing.T) {
	store := &memStore{objs: map[string]obj{
		"weeb/posters/one": {jpegOf(680, 1000), "image/jpeg", map[string]string{"source-length": "1"}},
	}}
	up := &fakeUp{}
	enc, pg, p := webpPipeline(store, up)

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil || res.Outcome != Upscaled || res.NewWidth != 1000 {
		t.Fatalf("%+v %v", res, err)
	}
	key := store.objs["weeb/posters/one"]
	if key.ct != "image/webp" || key.meta[MetaDisplayFormat] != "webp" || key.meta[MetaDisplayVariants] != "320,640" || key.meta[MetaDisplayWidth] != "1000" {
		t.Errorf("key: ct=%s meta=%v", key.ct, key.meta)
	}
	for _, w := range []int{320, 640} {
		v, ok := store.objs["weeb/posters/one"+VariantSuffix(w)]
		if !ok || widthOf(v.data) != w || v.ct != "image/webp" {
			t.Errorf("variant %d: present=%v width=%d ct=%s", w, ok, widthOf(v.data), v.ct)
		}
	}
	if _, full := store.objs["weeb/posters/one-full"]; !full {
		t.Error("the uncapped upscale should still be kept at -full")
	}
	// Three encodes: the display copy and two variants, all from the 2720px upscale.
	if len(enc.calls) != 3 {
		t.Errorf("encoder calls %v", enc.calls)
	}
	// Every written URL purged: the key and both variants.
	if len(pg.urls) != 3 {
		t.Errorf("purged %v", pg.urls)
	}
}

// The conversion walk: a key already upscaled and stored as JPEG, with the
// uncapped result beside it, is re-made as WebP from that result and left
// alone afterwards.
func TestAJPEGDisplayCopyIsReencodedAsWebPFromTheFullResult(t *testing.T) {
	done := map[string]string{"source-length": "1", MetaUpscaled: "m x4", MetaNormalized: "1", MetaDisplayWidth: "1000"}
	store := &memStore{objs: map[string]obj{
		"weeb/posters/one":      {jpegOf(1000, 1470), "image/jpeg", done},
		"weeb/posters/one-full": {jpegOf(2720, 4000), "image/jpeg", done},
		"weeb/posters/one-orig": {jpegOf(680, 1000), "image/jpeg", nil},
	}}
	up := &fakeUp{}
	enc, _, p := webpPipeline(store, up)

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil || res.Outcome != Reencoded || res.NewWidth != 1000 || up.calls != 0 {
		t.Fatalf("%+v %v calls=%d", res, err, up.calls)
	}
	key := store.objs["weeb/posters/one"]
	if key.ct != "image/webp" || key.meta[MetaDisplayFormat] != "webp" || key.meta[MetaUpscaled] != "m x4" {
		t.Errorf("key: ct=%s meta=%v", key.ct, key.meta)
	}
	if _, ok := store.objs["weeb/posters/one-w640"]; !ok {
		t.Error("variants missing after the conversion")
	}
	if len(enc.calls) == 0 || enc.calls[0] != "1000xwebp" {
		t.Errorf("display copy should come from the 2720px full result capped to 1000: %v", enc.calls)
	}
	if widthOf(store.objs["weeb/posters/one-full"].data) != 2720 {
		t.Error("the full result was touched")
	}

	res, _ = p.Handle(context.Background(), "weeb/posters/one")
	if res.Outcome != AlreadyUpscaled {
		t.Errorf("second pass: %+v", res)
	}
}

// Without a full result the original is the source; with neither, the key itself.
func TestTheConversionFallsBackToTheOriginalThenTheKey(t *testing.T) {
	norm := map[string]string{MetaNormalized: "1", MetaDisplayWidth: "1000"}
	store := &memStore{objs: map[string]obj{
		"weeb/posters/a":      {jpegOf(1000, 1470), "image/jpeg", norm},
		"weeb/posters/a-orig": {jpegOf(3000, 4400), "image/jpeg", nil},
		"weeb/posters/b":      {jpegOf(1000, 1470), "image/jpeg", norm},
	}}
	enc, _, p := webpPipeline(store, &fakeUp{})
	if res, err := p.Handle(context.Background(), "weeb/posters/a"); err != nil || res.Outcome != Reencoded {
		t.Fatalf("a: %+v %v", res, err)
	}
	if res, err := p.Handle(context.Background(), "weeb/posters/b"); err != nil || res.Outcome != Reencoded {
		t.Fatalf("b: %+v %v", res, err)
	}
	if store.objs["weeb/posters/a"].ct != "image/webp" || store.objs["weeb/posters/b"].ct != "image/webp" {
		t.Errorf("both keys should be webp now: %v", enc.calls)
	}
}

// The fast pass with the encoder: an oversized untouched object comes down
// as WebP with variants; a small source is still left for the model.
func TestWithoutTheModelOversizedObjectsBecomeWebP(t *testing.T) {
	store := &memStore{objs: map[string]obj{
		"weeb/wide":  {jpegOf(3000, 4400), "image/jpeg", nil},
		"weeb/small": {jpegOf(225, 337), "image/jpeg", nil},
	}}
	up := &fakeUp{}
	enc := &fakeEnc{}
	p := New(store, up, nil, Options{KeepOriginal: true, SkipUpscale: true, Encoder: enc, DisplayFormat: "webp"})

	res, err := p.Handle(context.Background(), "weeb/wide")
	if err != nil || res.Outcome != Downsized || res.NewWidth != 600 {
		t.Fatalf("wide: %+v %v", res, err)
	}
	if store.objs["weeb/wide"].ct != "image/webp" || widthOf(store.objs["weeb/wide-w320"].data) != 320 {
		t.Error("wide: not webp with its 320 variant")
	}
	if _, kept := store.objs["weeb/wide-orig"]; !kept {
		t.Error("original not kept")
	}
	res, _ = p.Handle(context.Background(), "weeb/small")
	if res.Outcome != AlreadyWide || up.calls != 0 || store.objs["weeb/small"].ct != "image/jpeg" {
		t.Errorf("small: %+v", res)
	}
}
