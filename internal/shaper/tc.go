// Package shaper builds and executes tc/netem/htb/ifb command sequences inside
// a shaper sidecar that shares its target's network namespace. Command
// construction is pure (BuildApply/BuildClear/BuildIngress) and unit-tested;
// execution goes through run.Runner.
//
// Design constraints (spec §4.2), enforced here:
//   - egress netem shapes the uplink directly (encoder's eth0);
//   - a truthful bandwidth cap stacks netem UNDER an htb class — never netem
//     `rate` alone;
//   - the download path is shaped by redirecting ingress to an IFB device and
//     attaching netem to the IFB's egress.
package shaper

import (
	"fmt"
	"strings"

	"streamwreck/internal/scenario"
)

// Dev is the interface shaped inside the sidecar's (shared) netns.
const Dev = "eth0"

// IFBDev is the intermediate functional block device for ingress shaping.
const IFBDev = "ifb0"

// BuildApply returns the tc command(s) that install the given impairment on the
// egress of dev. It uses del-then-add so every event is idempotent and fully
// reproducible with no add-vs-change state to track.
//
// Each returned element is a full argv (starting with "tc" or "sh") to exec.
func BuildApply(dev string, n *scenario.NetworkSpec) [][]string {
	// Always start from a clean slate.
	cmds := [][]string{delRoot(dev)}

	if n.Rate != nil && n.Accurate {
		// Accurate bandwidth: htb root shapes the rate, netem child adds
		// loss/delay/jitter. netem `rate` is deliberately NOT used here.
		cmds = append(cmds,
			tc("qdisc", "add", "dev", dev, "root", "handle", "1:", "htb", "default", "10"),
			tc("class", "add", "dev", dev, "parent", "1:", "classid", "1:10",
				"htb", "rate", n.Rate.TC()),
		)
		if child := netemArgs(n, false); len(child) > 0 {
			args := append([]string{"qdisc", "add", "dev", dev,
				"parent", "1:10", "handle", "10:", "netem"}, child...)
			cmds = append(cmds, tc(args...))
		}
		return cmds
	}

	// netem-only path (loss/delay/jitter, plus an approximate `rate` when the
	// scenario did not ask for accuracy).
	args := append([]string{"qdisc", "add", "dev", dev, "root", "netem"}, netemArgs(n, true)...)
	cmds = append(cmds, tc(args...))
	return cmds
}

// BuildClear returns the command to remove all egress qdiscs from dev.
func BuildClear(dev string) [][]string {
	return [][]string{delRoot(dev)}
}

// BuildIngress returns the command sequence that sets up the IFB ingress
// redirect and attaches netem to the IFB egress — the ONLY correct way to shape
// a download path (spec §4.2#3). Call BuildIngressTeardown to reverse it.
func BuildIngress(dev, ifb string, n *scenario.NetworkSpec) [][]string {
	cmds := [][]string{
		// Fail loudly if the ifb device cannot be created — a silent no-op here
		// is the #1 cause of "my downstream scenario had no effect" (§4.2#3).
		{"sh", "-c", fmt.Sprintf("ip link add %s type ifb && ip link set %s up", ifb, ifb)},
		tc("qdisc", "add", "dev", dev, "handle", "ffff:", "ingress"),
		tc("filter", "add", "dev", dev, "parent", "ffff:", "protocol", "ip", "u32",
			"match", "u32", "0", "0", "action", "mirred", "egress", "redirect", "dev", ifb),
	}
	args := append([]string{"qdisc", "add", "dev", ifb, "root", "netem"}, netemArgs(n, true)...)
	cmds = append(cmds, tc(args...))
	return cmds
}

// BuildIngressTeardown removes the ingress qdisc and IFB device.
func BuildIngressTeardown(dev, ifb string) [][]string {
	return [][]string{
		{"sh", "-c", fmt.Sprintf("tc qdisc del dev %s ingress 2>/dev/null; true", dev)},
		{"sh", "-c", fmt.Sprintf("ip link set %s down 2>/dev/null; ip link del %s 2>/dev/null; true", ifb, ifb)},
	}
}

// netemArgs renders the netem impairment tokens. When allowRate is true and the
// spec has a (non-accurate) rate, netem's approximate `rate` is included.
func netemArgs(n *scenario.NetworkSpec, allowRate bool) []string {
	var a []string
	if n.Delay != nil {
		a = append(a, "delay", ms(*n.Delay))
		if n.Jitter != nil {
			a = append(a, ms(*n.Jitter), "distribution", "normal")
		}
	}
	if n.Loss != nil {
		a = append(a, "loss", n.Loss.TC())
	}
	if n.Corrupt != nil {
		a = append(a, "corrupt", n.Corrupt.TC())
	}
	if n.Duplicate != nil {
		a = append(a, "duplicate", n.Duplicate.TC())
	}
	if n.Reorder != nil {
		// reorder requires a delay to be meaningful; tc needs a gap probability.
		a = append(a, "reorder", n.Reorder.TC())
	}
	if allowRate && n.Rate != nil && !n.Accurate {
		a = append(a, "rate", n.Rate.TC())
	}
	return a
}

func delRoot(dev string) []string {
	// Ignore "no such file" when nothing is installed yet.
	return []string{"sh", "-c", fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null; true", dev)}
}

func tc(args ...string) []string { return append([]string{"tc"}, args...) }

func ms(d scenario.Duration) string {
	return fmt.Sprintf("%dms", d.Std().Milliseconds())
}

// Render joins an argv for logging/tests.
func Render(cmds [][]string) string {
	lines := make([]string, len(cmds))
	for i, c := range cmds {
		lines[i] = strings.Join(c, " ")
	}
	return strings.Join(lines, "\n")
}
