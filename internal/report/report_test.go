package report

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReportRoundTrip(t *testing.T) {
	r := &Report{Scenario: "flaky-wifi", StartedAt: time.Now().UTC().Truncate(time.Second), Pass: true}
	r.Add("segment_duration", true, "all 20 segments align")
	r.Add("join_time", false, "3500 ms")
	join := int64(3500)
	r.JoinTimeMS = &join

	if r.Pass {
		t.Error("a failing check should flip Pass to false")
	}

	path := filepath.Join(t.TempDir(), "r.json")
	if err := r.Write(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scenario != r.Scenario || got.Pass != r.Pass || len(got.Checks) != 2 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.JoinTimeMS == nil || *got.JoinTimeMS != 3500 {
		t.Errorf("join time not preserved: %+v", got.JoinTimeMS)
	}
}

func TestWriteCreatesMissingDir(t *testing.T) {
	// A fresh clone has no reports/ dir; Write must create it.
	path := filepath.Join(t.TempDir(), "reports", "nested", "r.json")
	r := &Report{Scenario: "s", Pass: true}
	if err := r.Write(path); err != nil {
		t.Fatalf("Write into missing dir: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("report not written: %v", err)
	}
}

func TestSummary(t *testing.T) {
	r := &Report{Scenario: "s", Pass: true}
	r.Add("a", true, "")
	r.Add("b", true, "")
	if got := r.Summary(); got != "s: all 2 checks passed" {
		t.Errorf("summary = %q", got)
	}
	r.Add("c", false, "")
	if got := r.Summary(); got != "s: FAILED checks: c" {
		t.Errorf("failing summary = %q", got)
	}
}
