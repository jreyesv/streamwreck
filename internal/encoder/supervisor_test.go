package encoder

import (
	"context"
	"testing"

	"streamwreck/internal/run"
)

// The supervisor must keep every instance's log across relaunches, so the log
// for the instance that hit a disconnect survives a reconnect's relaunch.
func TestSupervisor_LogAccumulatesAcrossRelaunches(t *testing.T) {
	sup := NewSupervisor(run.NewFake(), "encoder")
	ctx := context.Background()

	if err := sup.Launch(ctx, baseScenario(), LaunchOpts{}); err != nil {
		t.Fatalf("launch 1: %v", err)
	}
	// A relaunch (as restart_encoder / reconnect does) must not drop instance #1.
	if err := sup.Launch(ctx, baseScenario(), LaunchOpts{}); err != nil {
		t.Fatalf("launch 2: %v", err)
	}

	// drain=0: the fake handles never "exit", so content is a placeholder, but the
	// count must reflect both instances in launch order.
	log := sup.Log(0)
	if len(log) != 2 {
		t.Fatalf("expected 2 encoder instances in the log, got %d", len(log))
	}
	if log[0].N != 1 || log[1].N != 2 {
		t.Errorf("instances should be numbered 1,2 in launch order, got %d,%d", log[0].N, log[1].N)
	}
}
