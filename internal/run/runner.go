// Package run is the single seam through which streamwreck touches Docker.
// Everything else (encoder, shaper, scte, verify) talks to a Runner, so the
// controller is unit-testable against a fake and there is exactly one place
// that shells out to `docker`.
package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	cmd    *exec.Cmd
	done   chan error
	Stderr *bytes.Buffer
}

// Wait blocks until the process exits.
func (h *Handle) Wait() error { return <-h.done }

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
}

// NewDocker returns a Runner bound to a specific compose file / project.
func NewDocker(composeFile, projectName string) Runner {
	return &dockerRunner{composeFile: composeFile, projectName: projectName}
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
	cmd.Stderr = stderr
	cmd.Stdout = os.Stderr // stream ffmpeg progress to our stderr for visibility
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start docker exec %s: %w", service, err)
	}
	h := &Handle{cmd: cmd, done: make(chan error, 1), Stderr: stderr}
	go func() { h.done <- cmd.Wait() }()
	return h, nil
}

func (d *dockerRunner) ComposeUp(ctx context.Context) error {
	return d.compose(ctx, "up", "-d", "--build")
}

func (d *dockerRunner) ComposeDown(ctx context.Context) error {
	return d.compose(ctx, "down", "-v")
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
