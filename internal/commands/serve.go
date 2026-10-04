package commands

import (
	"fmt"
	"log"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/weeb-vip/upscaler-service/config"
	"github.com/weeb-vip/upscaler-service/internal/server"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve POST /upscale and GET /healthz",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.Load()
		up := newUpscaler(cfg)
		if err := up.Check(); err != nil {
			return err
		}
		log.Printf("upscaler-service listening on :%d (model %s, scale %d, gpu %s, format %s)", cfg.Port, cfg.Model, cfg.Scale, cfg.GPU, cfg.Format)
		srv := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.Port),
			Handler: server.New(up, cfg.Format, cfg.MaxUploadBytes).Handler(),
			// No write timeout: one CPU upscale can legitimately run for minutes,
			// and the per-image timeout is the upscaler's own.
			ReadHeaderTimeout: 10 * 1e9,
		}
		return srv.ListenAndServe()
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
