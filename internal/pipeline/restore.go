package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"log"
	"strings"
)

// Restore outcomes, for one base key whose <key><OrigSuffix> copy exists.
const (
	Restored   Outcome = "restored"
	KeyIsFine  Outcome = "key-is-fine"
	NoOriginal Outcome = "no-usable-original"
)

// Restore puts <key><OrigSuffix> back at the key: when the key is all
// black (the ncnn Vulkan builds' failure mode), or always when all is set.
// No model runs, so a bucket's worth of black results is undone in
// minutes; the restored key carries no provenance, so the next walk
// upscales it again properly. The -full copy is left for that walk to
// overwrite.
func (p *Pipeline) Restore(ctx context.Context, key string, all bool) (Result, error) {
	res := Result{Key: key}
	if strings.HasSuffix(key, p.opts.OrigSuffix) || strings.HasSuffix(key, p.opts.FullSuffix) {
		res.Outcome = IsOriginal
		return res, nil
	}
	data, _, _, err := p.store.Get(ctx, key)
	if err != nil {
		return res, fmt.Errorf("get %s: %w", key, err)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		res.Width = cfg.Width
	}
	if !all && !isBlank(data) {
		res.Outcome = KeyIsFine
		return res, nil
	}
	orig, origMeta, ok, err := p.usableOriginal(ctx, key)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Outcome = NoOriginal
		return res, nil
	}
	if err := p.store.Put(ctx, key, orig, contentTypeOf(orig), origMeta); err != nil {
		return res, fmt.Errorf("restore %s: %w", key, err)
	}
	if p.purger != nil && p.opts.CDNBase != "" {
		url := strings.TrimSuffix(p.opts.CDNBase, "/") + "/" + key
		if err := p.purger.Purge(ctx, []string{url}); err != nil {
			log.Printf("purge %s: %v", url, err)
		}
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(orig))
	res.Outcome = Restored
	res.NewWidth = cfg.Width
	res.Bytes = len(orig)
	return res, nil
}
