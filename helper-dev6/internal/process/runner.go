package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const stderrLimit = 16 << 10

type limitedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := stderrLimit - b.b.Len()
	if remaining > 0 {
		if len(p) > remaining { p = p[:remaining] }
		_, _ = b.b.Write(p)
	}
	return n, nil
}

func (b *limitedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type Child struct {
	cmd    *exec.Cmd
	done   chan error
	stderr *limitedBuffer
	once   sync.Once
}

func Start(binary, configPath string) (*Child, error) {
	if binary == "" { return nil, errors.New("XRAY_BINARY is empty") }
	cmd := exec.Command(binary, "run", "-c", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := &limitedBuffer{}
	cmd.Stderr = stderr
	child := &Child{cmd: cmd, done: make(chan error, 1), stderr: stderr}
	if err := cmd.Start(); err != nil { return nil, fmt.Errorf("start Xray: %w", err) }
	go func() { child.done <- cmd.Wait(); close(child.done) }()
	return child, nil
}

func (c *Child) Exited() (bool, error) {
	select {
	case err := <-c.done:
		// Xray stderr can contain connection material. Keep it bounded in memory for
		// future redacted diagnostics, but never return it through the public API.
		if err != nil { return true, fmt.Errorf("Xray exited: %w", err) }
		return true, errors.New("Xray exited before test completed")
	default:
		return false, nil
	}
}

func (c *Child) Stop(ctx context.Context) error {
	var stopErr error
	c.once.Do(func() {
		if c.cmd.Process == nil { return }
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case err := <-c.done:
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) { stopErr = err }
			}
		case <-ctx.Done():
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-c.done:
			case <-time.After(2 * time.Second):
				stopErr = errors.New("Xray process could not be reaped")
			}
		}
	})
	return stopErr
}
