package main

import (
	"strings"
	"testing"

	"streamwreck/internal/scenario"
)

// every generated scenario must parse+validate.
func mustValidate(t *testing.T, yaml string) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("generated scenario invalid: %v\n---\n%s", err, yaml)
	}
	return s
}

func TestScaffoldYAML_WithPull(t *testing.T) {
	y, err := scaffoldYAML(scaffoldInput{
		Name: "t", Protocol: "rtmp", Profile: "flaky-uplink",
		Ingest: "rtmp://ingest.example.tv/app/KEY", Pull: "https://play.example.tv/x.m3u8",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := mustValidate(t, y)
	if s.Output.URL != "rtmp://ingest.example.tv/app/KEY" {
		t.Errorf("ingest url not set: %q", s.Output.URL)
	}
	if s.Verify == nil || !s.Verify.Enabled || s.Verify.Pull != "https://play.example.tv/x.m3u8" {
		t.Errorf("verify.pull not wired: %+v", s.Verify)
	}
	if len(s.Timeline) != 2 {
		t.Errorf("flaky-uplink should have 2 timeline events, got %d", len(s.Timeline))
	}
}

func TestScaffoldYAML_NoPullOmitsVerify(t *testing.T) {
	y, err := scaffoldYAML(scaffoldInput{
		Name: "t", Protocol: "rtmp", Profile: "clean",
		Ingest: "rtmp://x/y",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := mustValidate(t, y)
	if s.Verify != nil {
		t.Errorf("no pull URL should omit the verify block, got %+v", s.Verify)
	}
}

func TestScaffoldYAML_ReconnectProfile(t *testing.T) {
	y, err := scaffoldYAML(scaffoldInput{Name: "t", Protocol: "rtmp", Profile: "reconnect", Ingest: "rtmp://x/y"})
	if err != nil {
		t.Fatal(err)
	}
	s := mustValidate(t, y)
	var restarts, kills int
	for _, e := range s.Timeline {
		switch e.Action {
		case scenario.ActionRestartEncoder:
			restarts++
		case scenario.ActionKillEncoder:
			kills++
		}
	}
	if restarts != 1 || kills != 1 {
		t.Errorf("reconnect profile want 1 restart + 1 kill, got %d/%d", restarts, kills)
	}
}

func TestScaffoldYAML_SourceFile(t *testing.T) {
	y, _ := scaffoldYAML(scaffoldInput{Name: "t", Protocol: "rtmp", Profile: "clean",
		Ingest: "rtmp://x/y", SourceFile: "game.mp4"})
	s := mustValidate(t, y)
	if s.Source.Type != scenario.SourceFile || s.Source.File != "/media/game.mp4" {
		t.Errorf("source-file not wired: type=%q file=%q", s.Source.Type, s.Source.File)
	}
}

func TestScaffoldYAML_RejectsBadProfile(t *testing.T) {
	if _, err := scaffoldYAML(scaffoldInput{Name: "t", Protocol: "rtmp", Profile: "bogus", Ingest: "rtmp://x/y"}); err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestDefaultProtocolInference(t *testing.T) {
	if defaultProtocol("srt://x:9000") != "srt" {
		t.Error("srt url should infer srt")
	}
	if defaultProtocol("rtmp://x/y") != "rtmp" {
		t.Error("rtmp url should infer rtmp")
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"rtmp://ingest.example.tv/app/KEY":       "ingest.example.tv",
		"srt://host:8890?streamid=publish:x":     "host",
		"https://play.example.tv/hls/index.m3u8": "play.example.tv",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScaffoldMentionsStagingGuidance(t *testing.T) {
	y, _ := scaffoldYAML(scaffoldInput{Name: "t", Protocol: "rtmp", Profile: "clean", Ingest: "rtmp://x/y"})
	if !strings.Contains(y, "staging") && !strings.Contains(y, "STAGING") {
		t.Error("generated scenario should nudge the user toward a staging channel")
	}
}
