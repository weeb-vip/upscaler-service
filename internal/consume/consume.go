// Package consume runs the pipeline over image-sync's image-stored events.
//
// Same shape as image-sync's own NATS consumer: a main processor with a
// backoff-retry middleware that parks failures on <subject>-retry, and a
// retry processor that drains that subject and gives up to <subject>-dlq.
package consume

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/ThatCatDev/ep/v2/drivers"
	epNats "github.com/ThatCatDev/ep/v2/drivers/nats"
	"github.com/ThatCatDev/ep/v2/event"
	"github.com/ThatCatDev/ep/v2/middlewares/nats/backoffretry"
	"github.com/ThatCatDev/ep/v2/processor"
	"golang.org/x/sync/errgroup"

	"github.com/weeb-vip/upscaler-service/internal/pipeline"
)

const (
	maxRetries     = 3
	retryHeaderKey = "retry"
)

// StoredEvent mirrors image-sync's announcement. Path is prefix-relative
// and leading-slashed, as image-sync's storage takes it.
type StoredEvent struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	ID          string `json:"id"`
	SourceURL   string `json:"source_url"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
}

// KeyFor turns an announcement into the bucket key the pipeline works on.
func KeyFor(prefix string, ev StoredEvent) string {
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(ev.Path, "/")
}

type Config struct {
	URL               string
	ConsumerGroupName string
	StreamName        string
	Offset            string
	Subject           string
	Workers           int
	// Prefix is the bucket prefix the announced paths live under.
	Prefix string
}

// Handler is what one event does; the pipeline, behind a semaphore.
type Handler func(ctx context.Context, ev StoredEvent) error

// Run blocks, consuming until ctx ends or a consumer fails.
func Run(ctx context.Context, cfg Config, handle Handler) error {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	// The upscaler is the bottleneck, not NATS: cap how many run at once.
	slots := make(chan struct{}, cfg.Workers)
	var mu sync.Mutex
	process := func(ctx context.Context, data event.Event[*epNats.Message, StoredEvent]) (event.Event[*epNats.Message, StoredEvent], error) {
		slots <- struct{}{}
		defer func() { <-slots }()
		mu.Lock()
		defer mu.Unlock()
		return data, handle(ctx, data.Payload)
	}

	newDriver := func(group string) drivers.Driver[*epNats.Message] {
		return epNats.NewNatsDriver(&epNats.Config{
			URL:                     cfg.URL,
			ConsumerGroupName:       group,
			StreamName:              cfg.StreamName,
			ConsumerAutoOffsetReset: &cfg.Offset,
		})
	}
	driver := newDriver(cfg.ConsumerGroupName)
	defer driver.Close()
	retryDriver := newDriver(cfg.ConsumerGroupName + "-retry")
	defer retryDriver.Close()

	retrySubject := cfg.Subject + "-retry"
	dlqSubject := cfg.Subject + "-dlq"

	main := processor.NewProcessor[*epNats.Message, StoredEvent](driver, cfg.Subject, process).
		AddMiddleware(backoffretry.NewBackoffRetry[StoredEvent](driver, backoffretry.Config{
			MaxRetries: maxRetries, HeaderKey: retryHeaderKey, RetryQueue: retrySubject,
		}).Process)
	retry := processor.NewProcessor[*epNats.Message, StoredEvent](retryDriver, retrySubject, process).
		AddMiddleware(backoffretry.NewBackoffRetry[StoredEvent](retryDriver, backoffretry.Config{
			MaxRetries: maxRetries, HeaderKey: retryHeaderKey, RetryQueue: dlqSubject,
		}).Process)

	log.Printf("consuming %s (retry %s, dlq %s) as %s, %d worker(s)", cfg.Subject, retrySubject, dlqSubject, cfg.ConsumerGroupName, cfg.Workers)
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return main.Run(groupCtx) })
	group.Go(func() error { return retry.Run(groupCtx) })
	if err := group.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("consuming: %w", err)
	}
	return nil
}

// PipelineHandler adapts the pipeline to events.
func PipelineHandler(p *pipeline.Pipeline, prefix string) Handler {
	return func(ctx context.Context, ev StoredEvent) error {
		key := KeyFor(prefix, ev)
		res, err := p.Handle(ctx, key)
		if err != nil {
			log.Printf("%s: %v", key, err)
			return err
		}
		switch res.Outcome {
		case pipeline.Upscaled, pipeline.Downsized, pipeline.Recompressed:
			log.Printf("%s: %s %dpx -> %dpx, %d bytes, %s", key, res.Outcome, res.Width, res.NewWidth, res.Bytes, res.Took.Round(1e6))
		default:
			log.Printf("%s: %s", key, res.Outcome)
		}
		return nil
	}
}
