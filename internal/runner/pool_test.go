package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stand-in serve.py: answers each line, "die" kills the process, "slow"
// never answers, "bad" is a handler error. Prints its pid so reuse shows.
const fake = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *die*) exit 1 ;;
    *slow*) sleep 30 ;;
    *bad*) echo '{"ok":false,"error":"ValueError: bad"}' ;;
    *) echo "{\"ok\":true,\"summary\":\"pid $$\"}" ;;
  esac
done
`

func script(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "serve.sh")
	if err := os.WriteFile(p, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAProcessServesManyRequests(t *testing.T) {
	p := New(script(t), 1)
	defer p.Close()
	a, err := p.Call(context.Background(), map[string]any{"op": "ping"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Call(context.Background(), map[string]any{"op": "ping"})
	if a != b || !strings.HasPrefix(a, "pid ") {
		t.Errorf("expected the same process twice: %q %q", a, b)
	}
}

func TestAHandlerErrorKeepsTheProcess(t *testing.T) {
	p := New(script(t), 1)
	defer p.Close()
	a, _ := p.Call(context.Background(), map[string]any{"op": "ping"})
	if _, err := p.Call(context.Background(), map[string]any{"op": "bad"}); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err %v", err)
	}
	b, _ := p.Call(context.Background(), map[string]any{"op": "ping"})
	if a != b {
		t.Errorf("process replaced after a handler error: %q %q", a, b)
	}
}

func TestADeadOrStuckProcessIsReplaced(t *testing.T) {
	p := New(script(t), 1)
	defer p.Close()
	a, _ := p.Call(context.Background(), map[string]any{"op": "ping"})
	if _, err := p.Call(context.Background(), map[string]any{"op": "die"}); err == nil {
		t.Fatal("a dead runner must be an error")
	}
	b, err := p.Call(context.Background(), map[string]any{"op": "ping"})
	if err != nil || a == b {
		t.Fatalf("not replaced after dying: %q %q %v", a, b, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := p.Call(ctx, map[string]any{"op": "slow"}); err == nil {
		t.Fatal("a stuck runner must time out")
	}
	c, err := p.Call(context.Background(), map[string]any{"op": "ping"})
	if err != nil || c == b {
		t.Fatalf("not replaced after timing out: %q %q %v", b, c, err)
	}
}

func TestThePoolNeverRunsMoreThanItsSize(t *testing.T) {
	p := New(script(t), 3)
	defer p.Close()
	var wg sync.WaitGroup
	seen := sync.Map{}
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := p.Call(context.Background(), map[string]any{"op": "ping"})
			if err == nil {
				seen.Store(s, true)
			}
		}()
	}
	wg.Wait()
	n := 0
	seen.Range(func(_, _ any) bool { n++; return true })
	if n > 3 || n == 0 {
		t.Errorf("%d distinct processes for a pool of 3", n)
	}
}
