package shaper

import (
	"context"
	"fmt"

	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
)

// Shaper applies impairment inside a sidecar service that shares its target's
// network namespace (e.g. "shaper" for the encoder egress, "player-shaper" for
// the player pull path).
type Shaper struct {
	runner  run.Runner
	service string
	dev     string
}

// New binds a Shaper to a sidecar service. dev is normally shaper.Dev ("eth0").
func New(r run.Runner, service, dev string) *Shaper {
	return &Shaper{runner: r, service: service, dev: dev}
}

// Apply installs a netem/htb impairment on the egress of the shared interface,
// or a full-link blackout (cut) / teardown (clear) per the spec's mode.
func (s *Shaper) Apply(ctx context.Context, n *scenario.NetworkSpec) error {
	switch {
	case n.Clear:
		return s.Clear(ctx)
	case n.Cut:
		return s.execAll(ctx, BuildCut(s.dev))
	default:
		return s.execAll(ctx, BuildApply(s.dev, n))
	}
}

// Clear removes all egress qdiscs.
func (s *Shaper) Clear(ctx context.Context) error {
	return s.execAll(ctx, BuildClear(s.dev))
}

// CheckDevice verifies the shaping interface is visible in the sidecar's netns.
// A failure means the sidecar is attached to a stale namespace — typically its
// target container (encoder/player) was recreated, giving it a fresh netns while
// the sidecar stayed bound to the old one. Without this check, every tc command
// silently fails and the scenario tests an UNSHAPED stream.
func (s *Shaper) CheckDevice(ctx context.Context) error {
	if _, err := s.runner.Exec(ctx, s.service, "tc", "qdisc", "show", "dev", s.dev); err != nil {
		return fmt.Errorf("shaper %q cannot see %s — its network namespace is stale (the target "+
			"container was likely rebuilt). Run `streamwreck down && streamwreck up` to reattach it",
			s.service, s.dev)
	}
	return nil
}

// ApplyIngress shapes the download path via the IFB redirect. Used by the
// player-shaper for verify.degrade_player.
//
// The ifb kernel module must be present in the host kernel. It ships with
// mainstream Linux distributions and CI runners, but Docker Desktop for Mac's
// LinuxKit kernel does not include it — there, downstream shaping is
// unavailable and this returns an actionable error rather than silently doing
// nothing.
func (s *Shaper) ApplyIngress(ctx context.Context, n *scenario.NetworkSpec) error {
	if err := s.execAll(ctx, BuildIngress(s.dev, IFBDev, n)); err != nil {
		return fmt.Errorf("%w\nhint: downstream (player pull) shaping needs the 'ifb' kernel "+
			"module; it is absent from Docker Desktop for Mac's kernel. Run on a Linux host, "+
			"or `modprobe ifb` on the Docker host, to enable degrade_player", err)
	}
	return nil
}

// TeardownIngress reverses ApplyIngress.
func (s *Shaper) TeardownIngress(ctx context.Context) error {
	return s.execAll(ctx, BuildIngressTeardown(s.dev, IFBDev))
}

func (s *Shaper) execAll(ctx context.Context, cmds [][]string) error {
	for _, c := range cmds {
		if _, err := s.runner.Exec(ctx, s.service, c...); err != nil {
			return fmt.Errorf("shaper %s: %w", s.service, err)
		}
	}
	return nil
}
