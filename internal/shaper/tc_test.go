package shaper

import (
	"strings"
	"testing"
	"time"

	"streamwreck/internal/scenario"
)

func dur(d time.Duration) *scenario.Duration { x := scenario.Duration(d); return &x }
func pct(f float64) *scenario.Percent        { x := scenario.Percent(f); return &x }
func rate(bps int64) *scenario.Bitrate       { x := scenario.Bitrate(bps); return &x }

func TestBuildApply_NetemOnly(t *testing.T) {
	n := &scenario.NetworkSpec{Delay: dur(80 * time.Millisecond), Jitter: dur(30 * time.Millisecond), Loss: pct(3)}
	out := Render(BuildApply(Dev, n))
	// Must del root first (idempotent), then add netem with delay/jitter/loss.
	if !strings.Contains(out, "qdisc del dev eth0 root") {
		t.Errorf("missing del root:\n%s", out)
	}
	want := "tc qdisc add dev eth0 root netem delay 80ms 30ms distribution normal loss 3%"
	if !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	// netem-only must NOT reference htb.
	if strings.Contains(out, "htb") {
		t.Errorf("netem-only path should not use htb:\n%s", out)
	}
}

func TestBuildApply_ApproximateRate(t *testing.T) {
	n := &scenario.NetworkSpec{Rate: rate(800_000)} // no accurate → netem rate
	out := Render(BuildApply(Dev, n))
	if !strings.Contains(out, "netem rate 800kbit") {
		t.Errorf("expected approximate netem rate:\n%s", out)
	}
	if strings.Contains(out, "htb") {
		t.Errorf("non-accurate rate should not stack htb:\n%s", out)
	}
}

func TestBuildApply_AccurateRateStacksHTB(t *testing.T) {
	n := &scenario.NetworkSpec{Rate: rate(800_000), Accurate: true, Loss: pct(5)}
	out := Render(BuildApply(Dev, n))
	// Accurate bandwidth: htb root with the rate, netem child for loss — and
	// crucially NO netem `rate` (that would double-shape / lie).
	if !strings.Contains(out, "htb rate 800kbit") {
		t.Errorf("expected htb rate cap:\n%s", out)
	}
	if !strings.Contains(out, "parent 1:10 handle 10: netem") {
		t.Errorf("expected netem child under htb class:\n%s", out)
	}
	if !strings.Contains(out, "loss 5%") {
		t.Errorf("expected loss on netem child:\n%s", out)
	}
	if strings.Contains(out, "netem rate") {
		t.Errorf("accurate path must not use netem rate:\n%s", out)
	}
}

func TestBuildIngress_UsesIFBRedirect(t *testing.T) {
	n := &scenario.NetworkSpec{Delay: dur(100 * time.Millisecond), Loss: pct(5)}
	out := Render(BuildIngress(Dev, IFBDev, n))
	for _, want := range []string{
		"ip link add ifb0 type ifb",
		"qdisc add dev eth0 handle ffff: ingress",
		"action mirred egress redirect dev ifb0",
		"qdisc add dev ifb0 root netem delay 100ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ingress path missing %q:\n%s", want, out)
		}
	}
}

func TestBuildClear(t *testing.T) {
	out := Render(BuildClear(Dev))
	if !strings.Contains(out, "qdisc del dev eth0 root") {
		t.Errorf("clear should delete root qdisc:\n%s", out)
	}
}
