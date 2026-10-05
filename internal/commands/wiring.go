package commands

import (
	"fmt"

	"github.com/weeb-vip/upscaler-service/config"
	"github.com/weeb-vip/upscaler-service/internal/bucket"
	"github.com/weeb-vip/upscaler-service/internal/pipeline"
	"github.com/weeb-vip/upscaler-service/internal/purge"
	"github.com/weeb-vip/upscaler-service/internal/upscaler"
)

func newUpscaler(cfg config.Config) *upscaler.Upscaler {
	return upscaler.New(upscaler.Options{
		Binary: cfg.Binary, ModelsDir: cfg.ModelsDir, Model: cfg.Model, Scale: cfg.Scale,
		GPU: cfg.GPU, Tile: cfg.Tile, Threads: cfg.Threads, Timeout: cfg.Timeout,
	})
}

// newPipeline wires the bucket, the runner and the purge from the config.
func newPipeline(cfg config.Config) (*pipeline.Pipeline, error) {
	up := newUpscaler(cfg)
	if err := up.Check(); err != nil {
		return nil, err
	}
	store, err := bucket.New(bucket.Config{
		Endpoint: cfg.Bucket.Endpoint, AccessKeyID: cfg.Bucket.AccessKeyID, SecretAccessKey: cfg.Bucket.SecretAccessKey,
		UseSSL: cfg.Bucket.UseSSL, Bucket: cfg.Bucket.Bucket,
	})
	if err != nil {
		return nil, fmt.Errorf("bucket: %w", err)
	}
	var purger purge.Purger
	if cf := purge.NewCloudflare(cfg.CloudflareZoneID, cfg.CloudflareToken); cf != nil {
		purger = cf
	}
	scales := make(map[pipeline.Kind]int, len(cfg.Pipeline.Scales))
	for kind, n := range cfg.Pipeline.Scales {
		scales[pipeline.Kind(kind)] = n
	}
	// Caps start from the defaults; a variable overrides one kind.
	widths := make(map[pipeline.Kind]int, len(pipeline.DefaultDisplayWidths))
	for kind, n := range pipeline.DefaultDisplayWidths {
		widths[kind] = n
	}
	for kind, n := range cfg.Pipeline.DisplayWidths {
		widths[pipeline.Kind(kind)] = n
	}
	return pipeline.New(store, up, purger, pipeline.Options{
		MinWidth: cfg.Pipeline.MinWidth, KeepOriginal: cfg.Pipeline.KeepOriginal, OrigSuffix: cfg.Pipeline.OrigSuffix,
		Format: cfg.Pipeline.Format, CDNBase: cfg.Pipeline.CDNBase, Model: cfg.Model, Scales: scales,
		DisplayWidths: widths, KeepFull: cfg.Pipeline.KeepFull, DisplayQuality: cfg.Pipeline.DisplayQuality,
		MaxBytes: cfg.Pipeline.MaxBytes,
	}), nil
}
