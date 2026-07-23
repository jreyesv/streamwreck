// Package controller orchestrates a single scenario run: it ensures the stack
// is up, authors SCTE (if any), launches the encoder, steps the timeline
// applying network/action events at their scheduled offsets, then runs
// verification and tears down. It talks only to the module abstractions
// (encoder.Supervisor, shaper.Shaper, verify.Verifier), which in turn talk to a
// run.Runner — so the controller is unit-testable end to end with a fake.
package controller

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"streamwreck/internal/encoder"
	"streamwreck/internal/report"
	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
	"streamwreck/internal/scte"
	"streamwreck/internal/shaper"
	"streamwreck/internal/status"
	"streamwreck/internal/verify"
)

// Service names must match deploy/docker-compose.yml.
const (
	svcEncoder      = "encoder"
	svcShaper       = "shaper"
	svcPlayer       = "player"
	svcPlayerShaper = "player-shaper"
)

// Controller wires the modules for one run.
type Controller struct {
	runner run.Runner
	enc    *encoder.Supervisor
	egress *shaper.Shaper
	pull   *shaper.Shaper
	scte   *scte.Authorer
	verf   *verify.Verifier
	log    func(format string, a ...any)

	// live dashboard (nil in tests / when not streaming).
	model *status.Model
	live  *status.Live

	// runDurOverride, when non-nil, replaces the timeline-derived run duration.
	// Used by tests to avoid the multi-minute hold; nil in production.
	runDurOverride *time.Duration

	// encoderGrace is how long the encoder must survive after launch before the
	// run proceeds (fail-fast on a dead encoder). Tests set it to 0.
	encoderGrace time.Duration

	// stopGrace is how long ffmpeg gets to finalize the stream after the SIGINT
	// on teardown (Ctrl-C / end of run) before it is force-killed. Tests set it
	// to 0 to avoid the wait.
	stopGrace time.Duration

	// noUI disables the live dashboard (set by tests to keep output clean).
	noUI bool
}

// encoderLogDrain is how long EncoderLog waits for the last, still-terminating
// encoder instance to exit before reading its buffer.
const encoderLogDrain = 1500 * time.Millisecond

// EncoderLog returns the ffmpeg stderr captured for each encoder instance this
// run (one per launch/relaunch), for post-run inspection via `run --encoder-log`.
// Call it after Run returns, when the dashboard and encoder are torn down.
func (c *Controller) EncoderLog() []encoder.LogInstance {
	return c.enc.Log(encoderLogDrain)
}

// New constructs a Controller from a Runner.
func New(r run.Runner) *Controller {
	c := &Controller{
		runner:       r,
		enc:          encoder.NewSupervisor(r, svcEncoder),
		egress:       shaper.New(r, svcShaper, shaper.Dev),
		pull:         shaper.New(r, svcPlayerShaper, shaper.Dev),
		scte:         scte.New(r, svcShaper),
		verf:         verify.New(r, svcPlayer),
		encoderGrace: 2 * time.Second,
		stopGrace:    3 * time.Second,
	}
	c.log = func(f string, a ...any) {
		// While the animated dashboard owns the terminal, routine logs would
		// corrupt it — suppress them (the dashboard conveys the state instead).
		if c.live != nil && c.live.TTY() {
			return
		}
		fmt.Fprintf(os.Stderr, "[streamwreck] "+f+"\n", a...)
	}
	return c
}

// launch (re)starts the encoder and re-attaches the progress reader to the new
// process so the dashboard keeps updating across restarts.
func (c *Controller) launch(ctx context.Context, s *scenario.Scenario, opts encoder.LaunchOpts) error {
	if err := c.enc.Launch(ctx, s, opts); err != nil {
		return err
	}
	c.readProgress()
	return nil
}

// readProgress streams the current encoder's ffmpeg -progress output into the
// dashboard model. Each call attaches to the latest process; old readers EOF
// when their process exits. No-op when there is no dashboard (tests).
func (c *Controller) readProgress() {
	if c.model == nil {
		return
	}
	h := c.enc.Handle()
	if h == nil || h.Stdout == nil {
		return
	}
	go status.ParseProgress(h.Stdout, c.model.SetMetrics)
}

// startDashboard builds the live status model from the scenario's static facts
// and starts rendering.
func (c *Controller) startDashboard(s *scenario.Scenario, runDur scenario.Duration) {
	res := s.Source.Resolution
	if res == "" {
		res = "1280x720"
	}
	fps := s.Source.FPS
	if fps <= 0 {
		fps = 30
	}
	targetKbps := float64(s.Encoder.VideoBitrate.BitsPerSecond()) / 1000
	c.model = status.NewModel(s.Name, res, fps, s.Encoder.GOP, targetKbps,
		string(s.Output.Protocol), hostFromURL(s.Output.URL), runDur.Std())
	c.live = status.NewLive(c.model)
	c.live.Start()
}

// Run executes the scenario end to end and returns the verification report (nil
// when verification is disabled). The caller maps a failing report to a
// non-zero exit code.
func (c *Controller) Run(ctx context.Context, s *scenario.Scenario) (*report.Report, error) {
	// launchOpts accumulates action-driven mutations that take effect on the
	// next (re)launch of the encoder (av_desync, pts_jump, keyframe_misalign).
	launchOpts := encoder.LaunchOpts{}

	gopDur := gopDurationSeconds(s)
	runDur := timelineDuration(s)
	if s.RunDuration != nil {
		runDur = *s.RunDuration // explicit `duration:` / --duration overrides the derived length
	}
	if c.runDurOverride != nil {
		runDur = scenario.Duration(*c.runDurOverride)
	}

	// Repair well-known ingest-URL mistakes (e.g. an Amazon IVS URL missing its
	// :443 port and /app/ path) before the encoder ever tries to connect.
	if fixed, note := scenario.NormalizeIngestURL(s.Output.URL); note != "" {
		c.log("%s\n  %s\n  -> %s", note, s.Output.URL, fixed)
		s.Output.URL = fixed
	}

	// Author the SCTE-35 cue schedule up front (validates threefive and the
	// cues themselves before the stream starts). The authored markers become the
	// verifier's ground truth.
	authored := scte.Schedule(s, gopDur, runDur)
	if len(authored) > 0 {
		if err := c.authorSCTE(ctx, s, authored); err != nil {
			c.log("warning: SCTE authoring failed: %v", err)
		}
	}

	// Preflight: if the scenario shapes the uplink, confirm the shaper can see
	// its interface before we start. A stale sidecar netns (after a container
	// rebuild) would otherwise make every impairment silently no-op — the
	// scenario would "pass" while testing an unshaped stream.
	if hasNetworkEvents(s) {
		if err := c.egress.CheckDevice(ctx); err != nil {
			return nil, err
		}
	}

	// Clean slate on the egress before we start.
	if err := c.egress.Clear(ctx); err != nil {
		c.log("warning: initial egress clear failed: %v", err)
	}

	// Optional: pre-impair the player pull path for the whole run.
	if s.Verify != nil && s.Verify.Enabled && s.Verify.DegradePlayer {
		c.log("degrading player pull path via IFB ingress")
		if err := c.pull.ApplyIngress(ctx, playerImpairment(s)); err != nil {
			c.log("warning: player-shaper ingress setup failed: %v", err)
		}
		defer func() { _ = c.pull.TeardownIngress(context.Background()) }()
	}

	// Launch the encoder at t=0.
	c.log("launching encoder: %s", s.Name)
	start := time.Now()
	if err := c.enc.Launch(ctx, s, launchOpts); err != nil {
		return nil, fmt.Errorf("launch encoder: %w", err)
	}
	// Teardown (normal end or Ctrl-C) stops the encoder gracefully so the stream
	// ends with a proper end-of-stream to the ingest, not an abrupt disconnect.
	defer func() { _ = c.enc.StopGraceful(context.Background(), c.stopGrace) }()

	// Fail fast: if the encoder dies right away (bad ingest URL, rejected stream
	// key, wrong protocol), abort now with its log instead of holding the whole
	// run and then failing verification against a stream that never existed.
	if err := c.enc.EnsureAlive(ctx, c.encoderGrace); err != nil {
		return nil, fmt.Errorf("encoder failed to start: %w", err)
	}

	// Bring up the live dashboard and attach the progress reader.
	if !c.noUI {
		c.startDashboard(s, runDur)
		defer c.live.Stop()
		c.readProgress()
	}

	// Step the timeline (events fire at their offsets from `start`).
	if err := c.stepTimeline(ctx, start, s, &launchOpts); err != nil {
		return nil, err
	}

	// Hold the stream to the end of the run so the origin has produced enough
	// manifest/segments (and SCTE cadence markers) for verification to observe.
	// Without this hold an empty-timeline scenario would verify before the first
	// segment exists.
	if err := holdUntil(ctx, start.Add(runDur.Std())); err != nil {
		return nil, err
	}

	// Verify.
	if s.Verify == nil || !s.Verify.Enabled {
		c.log("verification disabled; run complete")
		return nil, nil
	}
	exp := verify.Expectations{
		GOPDurationSeconds: gopDur,
		Discontinuities:    expectedDiscontinuities(s),
		AuthoredSCTE:       authored,
		PlaybackWindow:     20 * time.Second,
	}
	c.log("running verification (%d checks)", len(s.Verify.Checks))
	if c.model != nil {
		c.model.SetPhase("verifying")
	}
	rep, err := c.verf.Run(ctx, s, exp)
	if err != nil {
		return nil, err
	}
	if s.Verify.Report != "" {
		if werr := rep.Write(s.Verify.Report); werr != nil {
			c.log("warning: could not write report: %v", werr)
		} else {
			c.log("report written to %s", s.Verify.Report)
		}
	}
	return rep, nil
}

// authorSCTE builds each scheduled cue via threefive (validating the live
// authoring path) and logs the base64. These cues are what an injection strategy
// muxes into the mpegts output; landing them in the HLS manifest is the §12
// prototype item (see README — authoring is validated, live mux is host/format
// dependent).
func (c *Controller) authorSCTE(ctx context.Context, s *scenario.Scenario, markers []report.Marker) error {
	c.log("authoring %d SCTE-35 %s cue(s) via threefive", len(markers), s.SCTE.Type)
	for i, m := range markers {
		cue, err := c.scte.AuthorCue(ctx, s.SCTE.Type, m.PTSSeconds, s.SCTE.BreakDuration.Std().Seconds())
		if err != nil {
			return err
		}
		c.log("  cue[%d] pts=%.3fs on_keyframe=%v -> %s", i, m.PTSSeconds, m.OnKeyframe, cue)
	}
	return nil
}

// stepTimeline fires each event at its offset relative to start.
func (c *Controller) stepTimeline(ctx context.Context, start time.Time, s *scenario.Scenario, opts *encoder.LaunchOpts) error {
	events := append(scenario.Timeline(nil), s.Timeline...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })

	for _, e := range events {
		if err := holdUntil(ctx, start.Add(e.At.Std())); err != nil {
			return err
		}
		if err := c.fire(ctx, s, e, opts); err != nil {
			// A single event failure should not abort the whole run silently;
			// log and continue so verification still runs.
			c.log("event at %s failed: %v", e.At, err)
		}
	}
	return nil
}

// holdUntil blocks until the target time, honoring context cancellation.
func holdUntil(ctx context.Context, target time.Time) error {
	wait := time.Until(target)
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// fire dispatches one timeline event.
func (c *Controller) fire(ctx context.Context, s *scenario.Scenario, e scenario.Event, opts *encoder.LaunchOpts) error {
	switch {
	case e.Network != nil:
		desc := describeNetwork(e.Network)
		c.log("t=%s network: %s", e.At, desc)
		c.setNetwork(desc)
		return c.egress.Apply(ctx, e.Network)
	case e.SourceSwitch != nil:
		c.log("t=%s source_switch", e.At)
		opts.SourceOverride = e.SourceSwitch
		return c.launch(ctx, s, *opts)
	case e.Action != "":
		return c.fireAction(ctx, s, e, opts)
	}
	return nil
}

// setNetwork updates the dashboard's current-impairment line.
func (c *Controller) setNetwork(desc string) {
	if c.model != nil {
		c.model.SetNetwork(desc)
	}
}

// fireAction handles the encoder-level chaos actions (Phase 4).
func (c *Controller) fireAction(ctx context.Context, s *scenario.Scenario, e scenario.Event, opts *encoder.LaunchOpts) error {
	switch e.Action {
	case scenario.ActionRestartEncoder:
		c.log("t=%s restart_encoder", e.At)
		return c.launch(ctx, s, *opts) // Launch stops the prior instance first

	case scenario.ActionKillEncoder:
		dead := e.Params.Duration.Std()
		c.log("t=%s kill_encoder (dead %s)", e.At, dead)
		if err := c.enc.Stop(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dead):
		}
		return c.launch(ctx, s, *opts)

	case scenario.ActionReconnect:
		// Model a broadcaster losing internet and reconnecting — the flow an
		// ingest's disconnect protection exists to handle. The disconnect must look
		// like lost internet (silent), and the recovery must be a FRESH RTMP session
		// (a new connection with the same key), not the old one resuming — that is
		// what disconnect protection waits for.
		dead := e.Params.Duration.Std()
		c.log("t=%s reconnect (offline %s)", e.At, dead)
		// 1. Blackout the uplink egress so neither an RTMP unpublish nor the TCP
		//    close reaches the ingest: it sees pure silence and engages disconnect
		//    protection (slate) instead of ending the stream. Egress-only is enough
		//    here because we kill the encoder next — no frozen process to protect
		//    from an inbound RST, so no ingress drop (and no IFB/module) is needed.
		if err := c.egress.Apply(ctx, blackoutSpec()); err != nil {
			c.log("reconnect: egress blackout failed: %v", err)
		}
		c.setNetwork("offline (lost internet)")
		if err := c.enc.Stop(ctx); err != nil {
			return err
		}
		// 2. Stay offline for the dead period. This MUST be within the ingest's
		//    reconnect window, or the reconnect below lands after the stream has
		//    ended and starts a new one instead of resuming.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dead):
		}
		// 3. Restore the uplink and reconnect with a fresh publish (same key).
		if err := c.egress.Clear(ctx); err != nil {
			c.log("reconnect: egress clear failed: %v", err)
		}
		c.setNetwork("reconnecting")
		return c.launch(ctx, s, *opts)

	case scenario.ActionAVDesync:
		c.log("t=%s av_desync (offset %s)", e.At, e.Params.Offset)
		opts.AVOffset = *e.Params.Offset
		return c.launch(ctx, s, *opts)

	case scenario.ActionPTSJump:
		c.log("t=%s pts_jump (jump %s)", e.At, e.Params.Jump)
		opts.PTSJump = *e.Params.Jump
		return c.launch(ctx, s, *opts)

	case scenario.ActionKeyframeMisalign:
		// Shift the GOP phase by half a GOP so splices miss IDR frames.
		opts.GOPPhaseShift = s.Encoder.GOP / 2
		c.log("t=%s keyframe_misalign (phase +%d frames)", e.At, opts.GOPPhaseShift)
		return c.launch(ctx, s, *opts)
	}
	return fmt.Errorf("unknown action %q", e.Action)
}
