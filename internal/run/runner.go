// Package run is the single seam through which streamwreck touches Docker.
// Everything else (encoder, shaper, scte, verify) talks to a Runner, so the
// controller is unit-testable against a fake and there is exactly one place
// that shells out to `docker`.
package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner executes commands inside compose services and manages the stack.
type Runner interface {
	// Exec runs argv inside the named service's container and returns combined
	// stdout (stderr is folded into the returned error on failure).
	Exec(ctx context.Context, service string, argv ...string) (string, error)

	// Start launches argv inside the service without waiting for it to exit,
	// returning a Handle for supervision (used for the long-lived ffmpeg).
	Start(ctx context.Context, service string, argv ...string) (*Handle, error)

	// ComposeUp / ComposeDown bring the whole lab stack up (detached) / down.
	ComposeUp(ctx context.Context) error
	ComposeDown(ctx context.Context) error
}

// Handle supervises a detached in-container process started via Start.
type Handle struct {
	cmd     *exec.Cmd
	done    chan struct{} // closed when the process exits
	mu      sync.Mutex
	exitErr error
	Stderr  *bytes.Buffer
	// Stdout streams the process's stdout (ffmpeg's -progress output). Read it
	// to drive the live dashboard; it EOFs when the process exits.
	Stdout io.Reader
}

// Wait blocks until the process exits and returns its exit error.
func (h *Handle) Wait() error {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exitErr
}

// WaitFor reports whether the process exited within d. When exited is true, err
// is its exit error (possibly nil); when false, the process is still running.
// This is the basis for the controller's fail-fast on a dead encoder.
func (h *Handle) WaitFor(d time.Duration) (exited bool, err error) {
	if h == nil || h.done == nil {
		return false, nil
	}
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return true, h.exitErr
	case <-time.After(d):
		return false, nil
	}
}

// Stop signals the underlying `docker exec` process to terminate. Callers that
// need the in-container process reliably dead should also issue a targeted kill
// (see encoder supervision) since signal forwarding through docker exec is
// best-effort.
func (h *Handle) Stop() error {
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return nil
	}
	return h.cmd.Process.Kill()
}

// dockerRunner is the production Runner: it shells `docker` / `docker compose`.
type dockerRunner struct {
	composeFile string
	projectName string
	profiles    []string // compose profiles to activate (e.g. "lab" for the demo origin)
}

// NewDocker returns a Runner bound to a specific compose file / project. Any
// profiles are passed to `docker compose` so profile-gated services (the demo
// MediaMTX origin) start only when requested.
func NewDocker(composeFile, projectName string, profiles ...string) Runner {
	return &dockerRunner{composeFile: composeFile, projectName: projectName, profiles: profiles}
}

func (d *dockerRunner) service(service string) string {
	// docker compose derives the container name; exec by service via compose.
	return service
}

func (d *dockerRunner) composeArgs(extra ...string) []string {
	args := []string{"compose", "-f", d.composeFile}
	if d.projectName != "" {
		args = append(args, "-p", d.projectName)
	}
	for _, p := range d.profiles {
		args = append(args, "--profile", p)
	}
	return append(args, extra...)
}

func (d *dockerRunner) Exec(ctx context.Context, service string, argv ...string) (string, error) {
	args := d.composeArgs(append([]string{"exec", "-T", d.service(service)}, argv...)...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %w\nstderr: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (d *dockerRunner) Start(ctx context.Context, service string, argv ...string) (*Handle, error) {
	args := d.composeArgs(append([]string{"exec", "-T", d.service(service)}, argv...)...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	stderr := &bytes.Buffer{}
	// ffmpeg's logs/errors go to stderr — capture them for the fail-fast tail and
	// the final summary (streaming them live would corrupt the in-place
	// dashboard). Its -progress output goes to stdout, which drives the dashboard.
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe stdout for %s: %w", service, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start docker exec %s: %w", service, err)
	}
	h := &Handle{cmd: cmd, done: make(chan struct{}), Stderr: stderr, Stdout: stdout}
	go func() {
		err := cmd.Wait()
		h.mu.Lock()
		h.exitErr = err
		h.mu.Unlock()
		close(h.done)
	}()
	return h, nil
}

func (d *dockerRunner) ComposeUp(ctx context.Context) error {
	return d.compose(ctx, "up", "-d", "--build")
}

func (d *dockerRunner) ComposeDown(ctx context.Context) error {
	// --remove-orphans clears services whose profile isn't active (e.g. the demo
	// origin) so `down` always tears the whole project down.
	return d.compose(ctx, "down", "-v", "--remove-orphans")
}

func (d *dockerRunner) compose(ctx context.Context, extra ...string) error {
	args := d.composeArgs(extra...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
