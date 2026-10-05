package commands

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/weeb-vip/upscaler-service/config"
	"github.com/weeb-vip/upscaler-service/internal/bucket"
	"github.com/weeb-vip/upscaler-service/internal/pipeline"
)

var (
	backfillPrefix string
	backfillLimit  int
	backfillDryRun bool
)

// backfillCmd walks the bucket and runs the pipeline over everything under a
// prefix: the catalogue as it is today, which no event will ever re-announce.
// The pipeline's own skip rules (provenance, width, -orig copies) make it
// safe to run again.
var backfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Walk a bucket prefix and upscale every image that needs it",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		if backfillPrefix == "" {
			backfillPrefix = cfg.Bucket.Prefix + "/"
		}
		p, err := newPipeline(cfg)
		if err != nil {
			return err
		}
		store, err := bucket.New(bucket.Config{
			Endpoint: cfg.Bucket.Endpoint, AccessKeyID: cfg.Bucket.AccessKeyID, SecretAccessKey: cfg.Bucket.SecretAccessKey,
			UseSSL: cfg.Bucket.UseSSL, Bucket: cfg.Bucket.Bucket,
		})
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		counts := map[pipeline.Outcome]int{}
		seen, failed := 0, 0
		for e := range store.List(ctx, backfillPrefix) {
			if e.Err != nil {
				return fmt.Errorf("list %s: %w", backfillPrefix, e.Err)
			}
			if strings.HasSuffix(e.Key, cfg.Pipeline.OrigSuffix) {
				continue
			}
			seen++
			if backfillLimit > 0 && seen > backfillLimit {
				break
			}
			if backfillDryRun {
				log.Printf("would consider %s (%d bytes)", e.Key, e.Size)
				continue
			}
			res, err := p.Handle(ctx, e.Key)
			if err != nil {
				failed++
				log.Printf("%s: %v", e.Key, err)
				continue
			}
			counts[res.Outcome]++
			if res.Outcome == pipeline.Upscaled || res.Outcome == pipeline.Downsized || res.Outcome == pipeline.Recompressed {
				log.Printf("%s: %dpx -> %dpx, %d bytes, %s", e.Key, res.Width, res.NewWidth, res.Bytes, res.Took.Round(1e6))
			}
		}
		log.Printf("backfill done: seen=%d failed=%d outcomes=%v", seen, failed, counts)
		return nil
	},
}

func init() {
	backfillCmd.Flags().StringVar(&backfillPrefix, "prefix", "", "bucket prefix to walk (default MINIO_PREFIX/)")
	backfillCmd.Flags().IntVar(&backfillLimit, "limit", 0, "stop after this many objects (0 = all)")
	backfillCmd.Flags().BoolVar(&backfillDryRun, "dry-run", false, "list what would be considered, touch nothing")
	rootCmd.AddCommand(backfillCmd)
}
