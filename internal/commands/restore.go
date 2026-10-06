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
	restorePrefix string
	restoreLimit  int
	restoreDryRun bool
	restoreAll    bool
)

// restoreCmd walks the -orig copies under a prefix and puts each one back
// over a black key (or over every key with --all). It runs no model, so it
// is the quick way to undo the black results of the Vulkan builds before a
// backfill walk redoes them properly.
var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Put <key>-orig back over every all-black key under a prefix (--all: over every key)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		if restorePrefix == "" {
			restorePrefix = cfg.Bucket.Prefix + "/"
		}
		store, err := bucket.New(bucket.Config{
			Endpoint: cfg.Bucket.Endpoint, AccessKeyID: cfg.Bucket.AccessKeyID, SecretAccessKey: cfg.Bucket.SecretAccessKey,
			UseSSL: cfg.Bucket.UseSSL, Bucket: cfg.Bucket.Bucket,
		})
		if err != nil {
			return err
		}
		p, err := newPipelineWith(cfg, store)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		counts := map[pipeline.Outcome]int{}
		seen, failed := 0, 0
		for e := range store.List(ctx, restorePrefix) {
			if e.Err != nil {
				return fmt.Errorf("list %s: %w", restorePrefix, e.Err)
			}
			if !strings.HasSuffix(e.Key, cfg.Pipeline.OrigSuffix) {
				continue
			}
			key := strings.TrimSuffix(e.Key, cfg.Pipeline.OrigSuffix)
			seen++
			if restoreLimit > 0 && seen > restoreLimit {
				break
			}
			if restoreDryRun {
				log.Printf("would check %s", key)
				continue
			}
			res, err := p.Restore(ctx, key, restoreAll)
			if err != nil {
				failed++
				log.Printf("%s: %v", key, err)
				continue
			}
			counts[res.Outcome]++
			if res.Outcome == pipeline.Restored {
				log.Printf("%s: %dpx black result replaced by the %dpx original (%d bytes)", key, res.Width, res.NewWidth, res.Bytes)
			}
		}
		log.Printf("restore done: originals=%d failed=%d outcomes=%v", seen, failed, counts)
		return nil
	},
}

func init() {
	restoreCmd.Flags().StringVar(&restorePrefix, "prefix", "", "bucket prefix to walk (default MINIO_PREFIX/)")
	restoreCmd.Flags().IntVar(&restoreLimit, "limit", 0, "stop after this many originals (0 = all)")
	restoreCmd.Flags().BoolVar(&restoreDryRun, "dry-run", false, "list what would be checked, touch nothing")
	restoreCmd.Flags().BoolVar(&restoreAll, "all", false, "put the original back over every key, not only black ones")
	rootCmd.AddCommand(restoreCmd)
}
