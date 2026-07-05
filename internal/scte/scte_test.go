package scte

import (
	"testing"
	"time"

	"streamwreck/internal/scenario"
)

func scteScenario(misalign bool) *scenario.Scenario {
	return &scenario.Scenario{
		Name: "t",
		SCTE: &scenario.SCTE{
			Enabled:       true,
			Type:          "time_signal",
			Cadence:       scenario.Duration(30 * time.Second),
			Preroll:       scenario.Duration(4 * time.Second),
			BreakDuration: scenario.Duration(30 * time.Second),
			Misalign:      misalign,
		},
	}
}

func TestSchedule_CadenceAndPreroll(t *testing.T) {
	// gop=60 @30fps → 2s GOP. Run 120s → markers at 30,60,90 fire times, each
	// +4s preroll = splice PTS 34,64,94.
	got := Schedule(scteScenario(false), 2.0, scenario.Duration(120*time.Second))
	if len(got) != 3 {
		t.Fatalf("expected 3 markers, got %d", len(got))
	}
	wantPTS := []float64{34, 64, 94}
	for i, m := range got {
		if m.PTSSeconds != wantPTS[i] {
			t.Errorf("marker[%d] PTS = %v, want %v", i, m.PTSSeconds, wantPTS[i])
		}
		if m.Type != "time_signal" {
			t.Errorf("marker[%d] type = %q", i, m.Type)
		}
	}
}

func TestSchedule_AlignedLandsOnKeyframe(t *testing.T) {
	// 34s against a 2s GOP grid is a multiple of 2 → on keyframe.
	got := Schedule(scteScenario(false), 2.0, scenario.Duration(40*time.Second))
	if len(got) != 1 || !got[0].OnKeyframe {
		t.Fatalf("aligned splice should land on keyframe: %+v", got)
	}
}

func TestSchedule_MisalignOffsetsAndMissesKeyframe(t *testing.T) {
	aligned := Schedule(scteScenario(false), 2.0, scenario.Duration(40*time.Second))
	misaligned := Schedule(scteScenario(true), 2.0, scenario.Duration(40*time.Second))
	if misaligned[0].OnKeyframe {
		t.Error("misaligned splice must not report on-keyframe")
	}
	if misaligned[0].PTSSeconds == aligned[0].PTSSeconds {
		t.Error("misalign should shift the splice PTS off the aligned value")
	}
}

func TestSchedule_DisabledReturnsNil(t *testing.T) {
	s := &scenario.Scenario{Name: "t"}
	if got := Schedule(s, 2.0, scenario.Duration(120*time.Second)); got != nil {
		t.Errorf("disabled SCTE should return nil, got %v", got)
	}
}

func TestBuildAuthorScript_TypeSelection(t *testing.T) {
	if got := buildAuthorScript("splice_insert", 34, 30); !contains(got, "mk_splice_insert") {
		t.Errorf("splice_insert should build via mk_splice_insert: %s", got)
	}
	if got := buildAuthorScript("time_signal", 34, 30); !contains(got, "mk_time_signal") {
		t.Errorf("time_signal should build via mk_time_signal: %s", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
