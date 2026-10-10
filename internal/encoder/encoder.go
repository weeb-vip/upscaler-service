// Package encoder writes the display copy of an image through the runner's
// display.py: resized down to a width and encoded as WebP (or JPEG), with
// Pillow doing the work the Go standard library cannot (WebP has no encoder
// there, and its JPEG is baseline and ten to fifteen percent heavier).
package encoder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	// Binary is the display.py path (or a wrapper that runs it in a venv).
	Binary  string
	Timeout time.Duration
	// Pool, when set, encodes in a long-lived runner/serve.py.
	Pool Caller
}

// Caller is the runner pool's one method.
type Caller interface {
	Call(ctx context.Context, req map[string]any) (string, error)
}

type Encoder struct {
	opts Options
	run  func(ctx context.Context, cmd *exec.Cmd) error
}

func New(opts Options) *Encoder {
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Minute
	}
	return &Encoder{opts: opts, run: func(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }}
}

// Check reports whether the binary can be found.
func (e *Encoder) Check() error {
	if e.opts.Pool != nil {
		return nil
	}
	if e.opts.Binary == "" {
		return errors.New("display encoder binary not set")
	}
	if _, err := exec.LookPath(e.opts.Binary); err != nil {
		return fmt.Errorf("display encoder %q not found: %w", e.opts.Binary, err)
	}
	return nil
}

// Encode returns `src` resized down to at most `width` wide (0 keeps the
// size) in `format` at `quality`.
func (e *Encoder) Encode(ctx context.Context, src []byte, width int, format string, quality int) ([]byte, error) {
	dir, err := os.MkdirTemp("", "display-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.img")
	out := filepath.Join(dir, "out."+format)
	if err := os.WriteFile(in, src, 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	if e.opts.Pool != nil {
		if _, err := e.opts.Pool.Call(ctx, map[string]any{"op": "display", "in": in, "out": out, "width": width, "format": format, "quality": quality}); err != nil {
			return nil, fmt.Errorf("encode failed: %w", err)
		}
		data, err := os.ReadFile(out)
		if err != nil || len(data) == 0 {
			return nil, errors.New("encode produced no output")
		}
		return data, nil
	}
	cmd := exec.CommandContext(ctx, e.opts.Binary, "-i", in, "-o", out, "-w", strconv.Itoa(width), "-f", format, "-q", strconv.Itoa(quality))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := e.run(ctx, cmd); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("encode timed out after %s", e.opts.Timeout)
		}
		return nil, fmt.Errorf("encode failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("encode produced no output: %s", strings.TrimSpace(stderr.String()))
	}
	if len(data) == 0 {
		return nil, errors.New("encode produced an empty file")
	}
	return data, nil
}
