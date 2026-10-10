// Package runner keeps long-lived runner processes (runner/serve.py) and
// hands each request to an idle one. Starting Python, importing onnxruntime
// and loading the network is most of a short job's cost; a pool pays it once
// per process instead of once per image.
package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Pool runs up to Size copies of Command, started on first use.
type Pool struct {
	command string
	idle    chan *proc
	slots   chan struct{}
	mu      sync.Mutex
	all     []*proc
}

type proc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// New makes a pool of at most size processes running command (a path to
// serve.py or to a wrapper that runs it in a virtualenv).
func New(command string, size int) *Pool {
	if size <= 0 {
		size = 1
	}
	p := &Pool{command: command, idle: make(chan *proc, size), slots: make(chan struct{}, size)}
	for i := 0; i < size; i++ {
		p.slots <- struct{}{}
	}
	return p
}

// Check reports whether the command can be found.
func (p *Pool) Check() error {
	if _, err := exec.LookPath(p.command); err != nil {
		return fmt.Errorf("runner %q not found: %w", p.command, err)
	}
	return nil
}

type answer struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
	Error   string `json:"error"`
}

// Call sends one request and waits for its answer. A process that misbehaves
// (dies, writes garbage, outlives ctx) is killed and its slot freed, so the
// next call starts a fresh one; an error the handler reported keeps the
// process.
func (p *Pool) Call(ctx context.Context, req map[string]any) (string, error) {
	pr, err := p.acquire(ctx)
	if err != nil {
		return "", err
	}
	line, err := json.Marshal(req)
	if err != nil {
		p.release(pr)
		return "", err
	}
	type result struct {
		a   answer
		err error
	}
	done := make(chan result, 1)
	go func() {
		if _, err := pr.in.Write(append(line, '\n')); err != nil {
			done <- result{err: fmt.Errorf("write to runner: %w", err)}
			return
		}
		raw, err := pr.out.ReadBytes('\n')
		if err != nil {
			done <- result{err: fmt.Errorf("read from runner: %w", err)}
			return
		}
		var a answer
		if err := json.Unmarshal(raw, &a); err != nil {
			done <- result{err: fmt.Errorf("runner answered %q: %w", string(raw), err)}
			return
		}
		done <- result{a: a}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			p.discard(pr)
			return "", r.err
		}
		p.release(pr)
		if !r.a.OK {
			return "", errors.New(r.a.Error)
		}
		return r.a.Summary, nil
	case <-ctx.Done():
		p.discard(pr)
		return "", ctx.Err()
	}
}

func (p *Pool) acquire(ctx context.Context) (*proc, error) {
	select {
	case pr := <-p.idle:
		return pr, nil
	default:
	}
	select {
	case pr := <-p.idle:
		return pr, nil
	case <-p.slots:
		pr, err := p.start()
		if err != nil {
			p.slots <- struct{}{}
			return nil, err
		}
		return pr, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Pool) start() (*proc, error) {
	cmd := exec.Command(p.command)
	cmd.Stderr = os.Stderr
	// Its own process group, so a kill reaches whatever the wrapper started
	// (the venv script execs python, but a shell wrapper might not).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start runner %q: %w", p.command, err)
	}
	pr := &proc{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 64<<10)}
	p.mu.Lock()
	p.all = append(p.all, pr)
	p.mu.Unlock()
	return pr, nil
}

func (p *Pool) release(pr *proc) { p.idle <- pr }

func (p *Pool) discard(pr *proc) {
	_ = syscall.Kill(-pr.cmd.Process.Pid, syscall.SIGKILL)
	_ = pr.cmd.Process.Kill()
	_ = pr.cmd.Wait()
	p.mu.Lock()
	for i, q := range p.all {
		if q == pr {
			p.all = append(p.all[:i], p.all[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	p.slots <- struct{}{}
}

// Close ends every process.
func (p *Pool) Close() {
	p.mu.Lock()
	all := append([]*proc(nil), p.all...)
	p.all = nil
	p.mu.Unlock()
	for _, pr := range all {
		_ = pr.in.Close()
		_ = pr.cmd.Wait()
	}
}
