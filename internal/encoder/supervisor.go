package encoder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
)

// pidFile is where the launch wrapper records the in-container ffmpeg PID so the
// supervisor can kill exactly that process (docker-exec signal forwarding is
// best-effort; a pidfile is deterministic).
const pidFile = "/run/streamwreck-ffmpeg.pid"

// Supervisor launches and controls the encoder ffmpeg inside a container.
type Supervisor struct {
	runner  run.Runner
	service string
	handle  *run.Handle
}

// NewSupervisor binds a supervisor to a Runner and target service (e.g. "encoder").
func NewSupervisor(r run.Runner, service string) *Supervisor {
	return &Supervisor{runner: r, service: service}
}

// Launch (re)starts ffmpeg with the given scenario/opts. Any previous instance
// is stopped first so callers can use Launch directly for restart_encoder.
func (s *Supervisor) Launch(ctx context.Context, sc *scenario.Scenario, opts LaunchOpts) error {
	_ = s.Stop(ctx) // idempotent: ensure no prior instance lingers

	args, err := BuildArgs(sc, opts)
	if err != nil {
		return err
	}
	// Wrap so the shell records the PID and blocks until ffmpeg exits; the
	// blocking `wait` keeps our Handle alive for the process lifetime.
	script := fmt.Sprintf("ffmpeg %s & echo $! > %s; wait $!", shellJoin(args), pidFile)
	h, err := s.runner.Start(ctx, s.service, "sh", "-c", script)
	if err != nil {
		return fmt.Errorf("launch encoder: %w", err)
	}
	s.handle = h
	return nil
}

// Stop kills the in-container ffmpeg (by pidfile, then a defensive pkill) and
// tears down the supervising exec. Safe to call when nothing is running.
func (s *Supervisor) Stop(ctx context.Context) error {
	// Kill the exact PID, then sweep any stragglers so no orphan encoder
	// survives a crash (see plan: defensive pkill on teardown).
	_, _ = s.runner.Exec(ctx, s.service, "sh", "-c",
		fmt.Sprintf("kill $(cat %s 2>/dev/null) 2>/dev/null; pkill -f ffmpeg 2>/dev/null; true", pidFile))
	if s.handle != nil {
		_ = s.handle.Stop()
		s.handle = nil
	}
	return nil
}

// EnsureAlive fails fast if the encoder exits within grace of launch — the
// signature of a bad ingest URL, rejected stream key, or wrong protocol. The
// returned error includes the tail of ffmpeg's own log so the cause is visible
// without a manual re-run.
func (s *Supervisor) EnsureAlive(ctx context.Context, grace time.Duration) error {
	if s.handle == nil {
		return fmt.Errorf("encoder is not running")
	}
	exited, werr := s.handle.WaitFor(grace)
	if !exited {
		return nil
	}
	tail := ""
	if s.handle.Stderr != nil {
		tail = lastLines(s.handle.Stderr.String(), 15)
	}
	msg := fmt.Sprintf("encoder exited within %s of launch", grace)
	if werr != nil {
		msg += " (" + werr.Error() + ")"
	}
	if tail != "" {
		msg += "\n--- ffmpeg output ---\n" + tail
	}
	return fmt.Errorf("%s", msg)
}

// Handle exposes the current supervising handle (nil when stopped).
func (s *Supervisor) Handle() *run.Handle { return s.handle }

// lastLines returns the final n non-empty lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var kept []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
}

// shellJoin single-quote-escapes each arg for safe embedding in `sh -c`.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
