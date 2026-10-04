package commands

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/weeb-vip/upscaler-service/config"
	"github.com/weeb-vip/upscaler-service/internal/consume"
)

var consumeCmd = &cobra.Command{
	Use:   "consume",
	Short: "Upscale what image-sync announces on NATS, in place, keeping the original",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		p, err := newPipeline(cfg)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return consume.Run(ctx, consume.Config{
			URL: cfg.Nats.URL, ConsumerGroupName: cfg.Nats.ConsumerGroupName, StreamName: cfg.Nats.StreamName,
			Offset: cfg.Nats.Offset, Subject: cfg.Nats.Subject, Workers: cfg.Nats.Workers, Prefix: cfg.Bucket.Prefix,
		}, consume.PipelineHandler(p, cfg.Bucket.Prefix))
	},
}

func init() {
	rootCmd.AddCommand(consumeCmd)
}
