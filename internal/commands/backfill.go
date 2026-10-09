package commands

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/weeb-vip/upscaler-service/config"
	"github.com/weeb-vip/upscaler-service/internal/bucket"
	"github.com/weeb-vip/upscaler-service/internal/pipeline"
)

var (
	backfillPrefix  string
	backfillLimit   int
	backfillDryRun  bool
	backfillWorkers int
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

		// Keys are independent, so they are handled by a pool: one worker is
		// the in-cluster default (the runner already uses every core it is
		// given), more is for a machine whose runner leaves cores idle, such
		// as a laptop driving a GPU.
		if backfillWorkers <= 0 {
			backfillWorkers = 1
		}
		var (
			mu     sync.Mutex
			wg     sync.WaitGroup
			counts = map[pipeline.Outcome]int{}
			failed int
			keys   = make(chan string)
		)
		for i := 0; i < backfillWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for key := range keys {
					res, err := p.Handle(ctx, key)
					mu.Lock()
					if err != nil {
						failed++
						log.Printf("%s: %v", key, err)
					} else {
						counts[res.Outcome]++
						if res.Outcome == pipeline.Upscaled || res.Outcome == pipeline.Repaired || res.Outcome == pipeline.Downsized || res.Outcome == pipeline.Recompressed {
							log.Printf("%s: %dpx -> %dpx, %d bytes, %s", key, res.Width, res.NewWidth, res.Bytes, res.Took.Round(1e6))
						}
					}
					mu.Unlock()
				}
			}()
		}
		seen := 0
		var listErr error
		for e := range store.List(ctx, backfillPrefix) {
			if e.Err != nil {
				listErr = fmt.Errorf("list %s: %w", backfillPrefix, e.Err)
				break
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
			select {
			case keys <- e.Key:
			case <-ctx.Done():
				listErr = ctx.Err()
			}
			if listErr != nil {
				break
			}
		}
		close(keys)
		wg.Wait()
		log.Printf("backfill done: seen=%d failed=%d workers=%d outcomes=%v", seen, failed, backfillWorkers, counts)
		return listErr
	},
}

func init() {
	backfillCmd.Flags().StringVar(&backfillPrefix, "prefix", "", "bucket prefix to walk (default MINIO_PREFIX/)")
	backfillCmd.Flags().IntVar(&backfillLimit, "limit", 0, "stop after this many objects (0 = all)")
	backfillCmd.Flags().BoolVar(&backfillDryRun, "dry-run", false, "list what would be considered, touch nothing")
	backfillCmd.Flags().IntVar(&backfillWorkers, "workers", 1, "keys handled at once; more than 1 for a machine whose runner leaves cores idle")
	rootCmd.AddCommand(backfillCmd)
}
