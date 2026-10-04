package upscaler

import (
	"context"
	"os/exec"
)

// SetRunner swaps the command executor. For tests in other packages, which
// cannot reach the unexported field; production code never calls it.
func SetRunner(u *Upscaler, run func(ctx context.Context, cmd *exec.Cmd) error) {
	u.run = run
}
