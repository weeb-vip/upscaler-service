package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/weeb-vip/upscaler-service/config"
)

var upscaleCmd = &cobra.Command{
	Use:   "upscale <input> <output>",
	Short: "Upscale one image file; the output's extension picks png, jpg or webp",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		up := newUpscaler(cfg)
		if err := up.Check(); err != nil {
			return err
		}
		start := time.Now()
		if err := up.File(context.Background(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("%s -> %s (%s, x%d, %s)\n", args[0], args[1], cfg.Model, cfg.Scale, time.Since(start).Round(time.Millisecond))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(upscaleCmd)
}
