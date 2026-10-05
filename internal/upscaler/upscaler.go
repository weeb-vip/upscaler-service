// Package upscaler runs Real-ESRGAN over an image.
//
// It shells out to realesrgan-ncnn-vulkan rather than binding the network
// itself: the ncnn build is a single static executable that runs on a GPU
// through Vulkan or on a CPU through a software Vulkan driver, needs no Python
// and no CUDA, and is what the Real-ESRGAN authors ship. Everything here is
// the plumbing around one invocation of it.
package upscaler

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

// Options configure one Upscaler. Zero values fall back to the binary's defaults.
type Options struct {
	Binary    string
	ModelsDir string
	Model     string
	Scale     int
	// GPU is "auto", or a device index; "-1" forces the CPU.
	GPU  string
	Tile int
	// Threads is the binary's -j load:proc:save. Processing threads are
	// what lavapipe's memory scales with: each one carries its own working
	// set, so fewer threads is the lever when a run is OOM-killed.
	Threads string
	Timeout time.Duration
}

// Formats the binary can write, by extension.
var Formats = map[string]bool{"png": true, "jpg": true, "webp": true}

type Upscaler struct {
	opts Options
	// run executes the prepared command. Swapped in tests for a fake.
	run func(ctx context.Context, cmd *exec.Cmd) error
}

func New(opts Options) *Upscaler {
	if opts.Binary == "" {
		opts.Binary = "realesrgan-ncnn-vulkan"
	}
	if opts.Model == "" {
		opts.Model = "realesr-general-x4v3"
	}
	if opts.Scale == 0 {
		opts.Scale = 2
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	return &Upscaler{opts: opts, run: func(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }}
}

// Options reports the settings in force, for the health endpoint and logs.
func (u *Upscaler) Options() Options { return u.opts }

// Check confirms the binary can be found. The server refuses to start without it.
func (u *Upscaler) Check() error {
	if _, err := exec.LookPath(u.opts.Binary); err != nil {
		return fmt.Errorf("upscaler binary %q not found: %w", u.opts.Binary, err)
	}
	return nil
}

// Args is the command line for one file, in the binary's own flag spelling.
// Exposed so a test can pin it: a flag that drifts here silently produces
// the wrong model or scale with no error from the binary. A scale of 0
// means the configured default; the x4 models accept 2 and 3 as well, by
// downscaling their output.
func (u *Upscaler) Args(in, out string, scale int) []string {
	if scale <= 0 {
		scale = u.opts.Scale
	}
	args := []string{"-i", in, "-o", out, "-n", u.opts.Model, "-s", strconv.Itoa(scale)}
	if u.opts.ModelsDir != "" {
		args = append(args, "-m", u.opts.ModelsDir)
	}
	if u.opts.GPU != "" && u.opts.GPU != "auto" {
		args = append(args, "-g", u.opts.GPU)
	}
	if u.opts.Tile > 0 {
		args = append(args, "-t", strconv.Itoa(u.opts.Tile))
	}
	if u.opts.Threads != "" {
		args = append(args, "-j", u.opts.Threads)
	}
	if ext := strings.TrimPrefix(filepath.Ext(out), "."); Formats[ext] {
		args = append(args, "-f", ext)
	}
	return args
}

// File upscales one image on disk into another. The output's extension picks
// the format; scale 0 is the configured default.
func (u *Upscaler) File(ctx context.Context, in, out string, scale int) error {
	ctx, cancel := context.WithTimeout(ctx, u.opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, u.opts.Binary, u.Args(in, out, scale)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	start := time.Now()
	if err := u.run(ctx, cmd); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("upscale timed out after %s", u.opts.Timeout)
		}
		return fmt.Errorf("upscale failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return fmt.Errorf("upscale produced no output in %s: %s", time.Since(start).Round(time.Millisecond), strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Bytes upscales an in-memory image and returns the result in `format`;
// scale 0 is the configured default.
func (u *Upscaler) Bytes(ctx context.Context, image []byte, format string, scale int) ([]byte, error) {
	if !Formats[format] {
		return nil, fmt.Errorf("unsupported output format %q (png, jpg or webp)", format)
	}
	dir, err := os.MkdirTemp("", "upscale-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	// The binary sniffs the input by content, so the input extension is only
	// a courtesy.
	in := filepath.Join(dir, "in.img")
	out := filepath.Join(dir, "out."+format)
	if err := os.WriteFile(in, image, 0o600); err != nil {
		return nil, err
	}
	if err := u.File(ctx, in, out, scale); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}
