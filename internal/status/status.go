package status

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Model holds everything the dashboard displays. All mutation goes through the
// setters, which are safe for concurrent use (the progress reader and the
// timeline stepper update it from different goroutines than the renderer).
type Model struct {
	Name string

	// Static stream facts (from the scenario).
	Resolution string
	FPS        int
	GOP        int
	TargetKbps float64
	Protocol   string
	Host       string

	mu      sync.Mutex
	metrics Metrics
	network string
	phase   string
	start   time.Time
	total   time.Duration
	spin    int
}

// NewModel builds a model with the run's static facts.
func NewModel(name, resolution string, fps, gop int, targetKbps float64, protocol, host string, total time.Duration) *Model {
	return &Model{
		Name: name, Resolution: resolution, FPS: fps, GOP: gop,
		TargetKbps: targetKbps, Protocol: protocol, Host: host,
		phase: "streaming", start: time.Now(), total: total,
		network: "none",
	}
}

func (m *Model) SetMetrics(x Metrics) { m.mu.Lock(); m.metrics = x; m.mu.Unlock() }
func (m *Model) SetNetwork(s string)  { m.mu.Lock(); m.network = s; m.mu.Unlock() }
func (m *Model) SetPhase(s string)    { m.mu.Lock(); m.phase = s; m.mu.Unlock() }
func (m *Model) tick()                { m.mu.Lock(); m.spin++; m.mu.Unlock() }

// keyframeSeconds is the derived keyframe interval.
func (m *Model) keyframeSeconds() float64 {
	if m.FPS <= 0 {
		return 0
	}
	return float64(m.GOP) / float64(m.FPS)
}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// lines renders the dashboard as a slice of styled lines. done draws the final
// (static) frame.
func (m *Model) lines(done bool) []string {
	m.mu.Lock()
	met := m.metrics
	network := m.network
	phase := m.phase
	elapsed := time.Since(m.start)
	total := m.total
	spin := string(spinner[m.spin%len(spinner)])
	m.mu.Unlock()

	head := spin
	if done {
		head = clr(green, "✓")
	}

	pct := 0.0
	if m.TargetKbps > 0 && met.Valid {
		pct = met.BitrateKbps / m.TargetKbps * 100
	}

	kf := fmt.Sprintf("%.1fs", m.keyframeSeconds())
	statFacts := fmt.Sprintf("%s · %dfps · keyframe %s · target %s · %s",
		m.Resolution, m.FPS, kf, mbps(m.TargetKbps), m.Protocol)

	var bitrate, rate string
	if met.Valid {
		bitrate = fmt.Sprintf("%s  %s  %s", pad(fmt.Sprintf("%.0f kbps", met.BitrateKbps), 11),
			bar(pct, 18), clr(pctColor(pct), fmt.Sprintf("%3.0f%%", pct)))
		rate = fmt.Sprintf("%.1ffps · speed %s · %d frames · %s dropped",
			met.FPS, speedStr(met.Speed), met.Frames, droppedStr(met.Dropped))
	} else {
		bitrate = clr(dim, "connecting…")
		rate = clr(dim, "—")
	}

	return []string{
		"",
		fmt.Sprintf("  %s %s  %s      %s %s / %s",
			head, clr(bold, "streamwreck"), clr(cyan, m.Name),
			clr(dim, phase), fmtClock(elapsed), fmtClock(total)),
		"",
		fmt.Sprintf("  %s   %s", label("stream"), clr(dim, statFacts)),
		fmt.Sprintf("  %s   %s", label("bitrate"), bitrate),
		fmt.Sprintf("  %s   %s", label("rate"), clr(dim, rate)),
		fmt.Sprintf("  %s   %s", label("network"), networkStr(network)),
	}
}

// plainLine is the non-TTY one-liner (no ANSI).
func (m *Model) plainLine() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	metric := "connecting"
	if m.metrics.Valid {
		metric = fmt.Sprintf("%.0fkbps %.1ffps %.2fx", m.metrics.BitrateKbps, m.metrics.FPS, m.metrics.Speed)
	}
	return fmt.Sprintf("[streamwreck] %s · %s/%s · %s · net:%s",
		m.Name, fmtClock(time.Since(m.start)), fmtClock(m.total), metric, m.network)
}

// ---- formatting helpers ----

func label(s string) string { return clr(dim, pad(s, 7)) }

func mbps(kbps float64) string {
	if kbps <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f Mbps", kbps/1000)
}

func fmtClock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func speedStr(s float64) string {
	txt := fmt.Sprintf("%.2f×", s)
	if s > 0 && s < 0.95 { // falling behind realtime = trouble
		return clr(red, txt)
	}
	return clr(white, txt)
}

func droppedStr(n int) string {
	if n > 0 {
		return clr(yellow, fmt.Sprintf("%d", n))
	}
	return "0"
}

func networkStr(s string) string {
	if s == "" || s == "none" || s == "clear" {
		return clr(dim, "clean")
	}
	return clr(yellow, s)
}

// bar draws a proportional meter, colored by how close bitrate is to target.
func bar(pct float64, width int) string {
	filled := int(pct/100*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return clr(pctColor(pct), strings.Repeat("█", filled)) +
		clr(dim, strings.Repeat("░", width-filled))
}

func pctColor(pct float64) string {
	switch {
	case pct >= 90:
		return green
	case pct >= 50:
		return yellow
	default:
		return red // starving
	}
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// ---- color ----

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	white  = "\033[37m"
)

var useColor = os.Getenv("NO_COLOR") == ""

func clr(code, s string) string {
	if !useColor {
		return s
	}
	return code + s + reset
}
