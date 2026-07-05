package scenario

import (
	"fmt"
	"strings"
)

// knownChecks are the verification checks the verifier understands (§5/§10).
var knownChecks = map[string]bool{
	"segment_duration":   true,
	"discontinuity_tags": true,
	"scte_markers":       true,
	"join_time":          true,
	"rebuffering":        true,
}

// Validate performs semantic checks beyond YAML shape. It powers
// `streamwreck validate` and is also called by Parse so a bad scenario never
// reaches the controller.
func (s *Scenario) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if strings.TrimSpace(s.Name) == "" {
		add("name is required")
	}

	// Source.
	switch s.Source.Type {
	case SourceTestSrc2, SourceSMPTE, SourceComplex:
	case SourceFile:
		if s.Source.File == "" {
			add("source.type=file requires source.file")
		}
	case "":
		add("source.type is required")
	default:
		add("source.type %q is not one of testsrc2|smpte|file|complex", s.Source.Type)
	}
	if s.Source.Type == SourceComplex && s.Source.Complexity != "" {
		switch s.Source.Complexity {
		case ComplexityLow, ComplexityMedium, ComplexityHigh:
		default:
			add("source.complexity %q is not one of low|medium|high", s.Source.Complexity)
		}
	}
	if s.Source.FPS <= 0 {
		add("source.fps must be positive")
	}

	// Encoder.
	if s.Encoder.VideoBitrate <= 0 {
		add("encoder.video_bitrate must be positive")
	}
	if s.Encoder.GOP <= 0 {
		add("encoder.gop must be positive")
	}

	// Output.
	switch s.Output.Protocol {
	case ProtocolRTMP, ProtocolSRT:
	case "":
		add("output.protocol is required")
	default:
		add("output.protocol %q is not one of rtmp|srt", s.Output.Protocol)
	}
	if s.Output.URL == "" {
		add("output.url is required")
	}

	// Timeline: each entry has exactly one directive; actions carry valid params.
	for i, e := range s.Timeline {
		errs = append(errs, e.validate(i)...)
	}

	// SCTE requires an mpegts-capable path (SRT); FLV/RTMP can't carry SCTE-35.
	if s.SCTE != nil && s.SCTE.Enabled {
		if s.SCTE.Type != "time_signal" && s.SCTE.Type != "splice_insert" {
			add("scte35.type %q is not one of time_signal|splice_insert", s.SCTE.Type)
		}
		if s.SCTE.Cadence <= 0 {
			add("scte35.cadence must be positive when scte35.enabled")
		}
		if s.Output.Protocol == ProtocolRTMP {
			add("scte35.enabled requires output.protocol=srt (RTMP/FLV cannot carry SCTE-35)")
		}
	}

	// Ad requires SCTE breaks to attach to, plus its own source+duration.
	if s.Ad != nil && s.Ad.Enabled {
		if s.SCTE == nil || !s.SCTE.Enabled {
			add("ad.enabled requires scte35.enabled (ads splice at SCTE breaks)")
		}
		if s.Ad.Duration <= 0 {
			add("ad.duration must be positive when ad.enabled")
		}
		if s.Ad.Source.Type == SourceFile && s.Ad.Source.File == "" {
			add("ad.source.type=file requires ad.source.file")
		}
	}

	// Verify.
	if s.Verify != nil && s.Verify.Enabled {
		if s.Verify.Pull == "" {
			add("verify.pull is required when verify.enabled")
		}
		for _, c := range s.Verify.Checks {
			if !knownChecks[c] {
				add("verify.checks contains unknown check %q", c)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("scenario %q is invalid:\n  - %s", s.Name, strings.Join(errs, "\n  - "))
	}
	return nil
}

func (e Event) validate(i int) []string {
	var errs []string
	label := fmt.Sprintf("timeline[%d] (at %s)", i, e.At)

	// Exactly one directive.
	set := 0
	if e.Network != nil {
		set++
	}
	if e.Action != "" {
		set++
	}
	if e.SourceSwitch != nil {
		set++
	}
	switch {
	case set == 0:
		errs = append(errs, label+": must have exactly one of network|action|source_switch (found none)")
	case set > 1:
		errs = append(errs, label+": must have exactly one of network|action|source_switch (found several)")
	}

	// Action-specific param requirements.
	switch e.Action {
	case "":
		// not an action entry
	case ActionRestartEncoder, ActionKeyframeMisalign:
		// no params required
	case ActionKillEncoder:
		if e.Params.Duration == nil {
			errs = append(errs, label+": action kill_encoder requires params.duration")
		}
	case ActionAVDesync:
		if e.Params.Offset == nil {
			errs = append(errs, label+": action av_desync requires params.offset")
		}
	case ActionPTSJump:
		if e.Params.Jump == nil {
			errs = append(errs, label+": action pts_jump requires params.jump")
		}
	default:
		errs = append(errs, fmt.Sprintf("%s: unknown action %q", label, e.Action))
	}

	// Rate accuracy hint: `accurate: true` without a rate does nothing.
	if e.Network != nil && e.Network.Accurate && e.Network.Rate == nil {
		errs = append(errs, label+": network.accurate=true has no effect without network.rate")
	}
	return errs
}
