// Package report defines the machine-diffable JSON verification report emitted
// by a run, plus a human-readable printer for `streamwreck report`.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Report is the top-level artifact written to verify.report and re-read by the
// `report` subcommand.
type Report struct {
	Scenario  string        `json:"scenario"`
	StartedAt time.Time     `json:"started_at"`
	Duration  string        `json:"duration"`
	Pass      bool          `json:"pass"`
	Checks    []CheckResult `json:"checks"`

	// Measurements captured regardless of pass/fail.
	JoinTimeMS      *int64 `json:"join_time_ms,omitempty"`
	RebufferCount   *int   `json:"rebuffer_count,omitempty"`
	RebufferMS      *int64 `json:"rebuffer_ms,omitempty"`
	Discontinuities int    `json:"discontinuities"`

	// SCTE marker diff, present when scte_markers ran.
	SCTE *SCTEDiff `json:"scte,omitempty"`
}

// CheckResult is the outcome of a single named verification check.
type CheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// SCTEDiff compares authored SCTE-35 markers against those observed in the
// manifest (count/timing/type/keyframe-landing).
type SCTEDiff struct {
	Authored []Marker `json:"authored"`
	Observed []Marker `json:"observed"`
	Matched  int      `json:"matched"`
	Missing  int      `json:"missing"`
	Extra    int      `json:"extra"`
}

// Marker is one SCTE-35 splice point.
type Marker struct {
	Type       string  `json:"type"` // time_signal | splice_insert
	PTSSeconds float64 `json:"pts_seconds"`
	OnKeyframe bool    `json:"on_keyframe"`
}

// Add appends a check result and updates the aggregate pass flag.
func (r *Report) Add(name string, pass bool, format string, a ...any) {
	r.Checks = append(r.Checks, CheckResult{Name: name, Pass: pass, Detail: fmt.Sprintf(format, a...)})
	if !pass {
		r.Pass = false
	}
}

// Write serializes the report to path as indented JSON, creating the parent
// directory if needed (a fresh clone has no reports/ dir).
func (r *Report) Write(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Load reads a report JSON file.
func Load(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse report: %w", err)
	}
	return &r, nil
}

// Print renders a human-friendly summary to w.
func (r *Report) Print(w *os.File) {
	status := "PASS"
	if !r.Pass {
		status = "FAIL"
	}
	fmt.Fprintf(w, "streamwreck report — %s  [%s]\n", r.Scenario, status)
	fmt.Fprintf(w, "started %s, ran %s\n\n", r.StartedAt.Format(time.RFC3339), r.Duration)

	for _, c := range r.Checks {
		mark := "✓"
		if !c.Pass {
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %-20s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Fprintln(w)

	if r.JoinTimeMS != nil {
		fmt.Fprintf(w, "  join time      : %d ms\n", *r.JoinTimeMS)
	}
	if r.RebufferCount != nil {
		ms := int64(0)
		if r.RebufferMS != nil {
			ms = *r.RebufferMS
		}
		fmt.Fprintf(w, "  rebuffering    : %d stalls, %d ms\n", *r.RebufferCount, ms)
	}
	fmt.Fprintf(w, "  discontinuities: %d\n", r.Discontinuities)

	if r.SCTE != nil {
		fmt.Fprintf(w, "\n  SCTE-35: %d matched, %d missing, %d extra\n",
			r.SCTE.Matched, r.SCTE.Missing, r.SCTE.Extra)
	}
}

// Summary returns a one-line status suitable for logs.
func (r *Report) Summary() string {
	var failed []string
	for _, c := range r.Checks {
		if !c.Pass {
			failed = append(failed, c.Name)
		}
	}
	if r.Pass {
		return fmt.Sprintf("%s: all %d checks passed", r.Scenario, len(r.Checks))
	}
	return fmt.Sprintf("%s: FAILED checks: %s", r.Scenario, strings.Join(failed, ", "))
}
