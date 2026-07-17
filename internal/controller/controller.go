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

	// runDurOverride, when non-nil, replaces the timeline-derived run duration.
	// Used by tests to avoid the multi-minute hold; nil in production.
	runDurOverride *time.Duration

	// encoderGrace is how long the encoder must survive after launch before the
	// run proceeds (fail-fast on a dead encoder). Tests set it to 0.
	encoderGrace time.Duration
}

// New constructs a Controller from a Runner.
func New(r run.Runner) *Controller {
	return &Controller{
		runner:       r,
		enc:          encoder.NewSupervisor(r, svcEncoder),
		egress:       shaper.New(r, svcShaper, shaper.Dev),
		pull:         shaper.New(r, svcPlayerShaper, shaper.Dev),
		scte:         scte.New(r, svcShaper),
		verf:         verify.New(r, svcPlayer),
		encoderGrace: 2 * time.Second,
		log:          func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[streamwreck] "+f+"\n", a...) },
	}
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
	defer func() { _ = c.enc.Stop(context.Background()) }()

	// Fail fast: if the encoder dies right away (bad ingest URL, rejected stream
	// key, wrong protocol), abort now with its log instead of holding the whole
	// run and then failing verification against a stream that never existed.
	if err := c.enc.EnsureAlive(ctx, c.encoderGrace); err != nil {
		return nil, fmt.Errorf("encoder failed to start: %w", err)
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
		c.log("t=%s network: %s", e.At, describeNetwork(e.Network))
		return c.egress.Apply(ctx, e.Network)
	case e.SourceSwitch != nil:
		c.log("t=%s source_switch", e.At)
		opts.SourceOverride = e.SourceSwitch
		return c.enc.Launch(ctx, s, *opts)
	case e.Action != "":
		return c.fireAction(ctx, s, e, opts)
	}
	return nil
}

// fireAction handles the encoder-level chaos actions (Phase 4).
func (c *Controller) fireAction(ctx context.Context, s *scenario.Scenario, e scenario.Event, opts *encoder.LaunchOpts) error {
	switch e.Action {
	case scenario.ActionRestartEncoder:
		c.log("t=%s restart_encoder", e.At)
		return c.enc.Launch(ctx, s, *opts) // Launch stops the prior instance first

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
		return c.enc.Launch(ctx, s, *opts)

	case scenario.ActionAVDesync:
		c.log("t=%s av_desync (offset %s)", e.At, e.Params.Offset)
		opts.AVOffset = *e.Params.Offset
		return c.enc.Launch(ctx, s, *opts)

	case scenario.ActionPTSJump:
		c.log("t=%s pts_jump (jump %s)", e.At, e.Params.Jump)
		opts.PTSJump = *e.Params.Jump
		return c.enc.Launch(ctx, s, *opts)

	case scenario.ActionKeyframeMisalign:
		// Shift the GOP phase by half a GOP so splices miss IDR frames.
		opts.GOPPhaseShift = s.Encoder.GOP / 2
		c.log("t=%s keyframe_misalign (phase +%d frames)", e.At, opts.GOPPhaseShift)
		return c.enc.Launch(ctx, s, *opts)
	}
	return fmt.Errorf("unknown action %q", e.Action)
}
