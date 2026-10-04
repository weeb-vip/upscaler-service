package pipeline

import (
	"bytes"
	"context"
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

type fakeUp struct{ calls int }

// Produces a PNG four times as wide as asked, whatever the format name.
func (f *fakeUp) Bytes(_ context.Context, in []byte, _ string) ([]byte, error) {
	f.calls++
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(in))
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, cfg.Width*4, cfg.Height*4)))
	return buf.Bytes(), nil
}

type fakePurge struct{ urls []string }

func (f *fakePurge) Purge(_ context.Context, urls []string) error {
	f.urls = append(f.urls, urls...)
	return nil
}

func jpegOf(w, h int) []byte {
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
	p := New(store, up, pg, Options{KeepOriginal: true, CDNBase: "https://cdn.weeb.vip", Model: "realesrgan-x4plus-anime"})
	return store, up, pg, p
}

func TestANarrowImageIsUpscaledInPlaceWithTheOriginalKeptBesideIt(t *testing.T) {
	store, up, pg, p := setup(225)

	res, err := p.Handle(context.Background(), "weeb/posters/one")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Upscaled || res.Width != 225 || res.NewWidth != 900 {
		t.Fatalf("result %+v", res)
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
	store, up, _, p := setup(1920)
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
