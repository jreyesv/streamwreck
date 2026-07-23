package scenario

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Scenario is the top-level declarative description of a streamwreck run.
type Scenario struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	Source   Source   `yaml:"source"`
	Encoder  Encoder  `yaml:"encoder"`
	Output   Output   `yaml:"output"`
	Timeline Timeline `yaml:"timeline"`

	// RunDuration, when set, is how long the stream runs before verification.
	// Unset (nil) means "derive from the timeline" (last event offset, floored
	// to 60s, plus a 30s tail). A `--duration` flag overrides this.
	RunDuration *Duration `yaml:"duration"`

	SCTE   *SCTE   `yaml:"scte35"`
	Ad     *Ad     `yaml:"ad"`
	Verify *Verify `yaml:"verify"`
}

// SourceType selects the ffmpeg lavfi source (or a mounted file).
type SourceType string

const (
	SourceTestSrc2 SourceType = "testsrc2"
	SourceSMPTE    SourceType = "smpte"
	SourceFile     SourceType = "file"
	SourceComplex  SourceType = "complex" // high-entropy motion → forces VBR to climb
)

// Complexity tunes the complex source's entropy (only used when type=complex).
type Complexity string

const (
	ComplexityLow    Complexity = "low"
	ComplexityMedium Complexity = "medium"
	ComplexityHigh   Complexity = "high"
)

type Source struct {
	Type            SourceType `yaml:"type"`
	File            string     `yaml:"file"`
	Resolution      string     `yaml:"resolution"`
	FPS             int        `yaml:"fps"`
	TimecodeOverlay bool       `yaml:"timecode_overlay"`
	Complexity      Complexity `yaml:"complexity"`
}

// Encoder mirrors the libx264/aac knobs the ffmpeg argv is built from.
type Encoder struct {
	VideoBitrate Bitrate `yaml:"video_bitrate"`
	Maxrate      Bitrate `yaml:"maxrate"`
	Bufsize      Bitrate `yaml:"bufsize"`
	GOP          int     `yaml:"gop"`
	KeyintMin    int     `yaml:"keyint_min"`
	Preset       string  `yaml:"preset"`
	Tune         string  `yaml:"tune"`
	AudioBitrate Bitrate `yaml:"audio_bitrate"`
	Resolution   string  `yaml:"resolution"` // used by ad-encoder profile overrides
}

// Protocol selects the uplink container/transport.
type Protocol string

const (
	ProtocolRTMP Protocol = "rtmp"
	ProtocolSRT  Protocol = "srt"
)

type Output struct {
	Protocol Protocol `yaml:"protocol"`
	URL      string   `yaml:"url"`
}

// Timeline is a list of events; Load sorts it ascending by offset.
type Timeline []Event

// ActionType enumerates the encoder-level chaos actions (§5).
type ActionType string

const (
	ActionRestartEncoder   ActionType = "restart_encoder"
	ActionKillEncoder      ActionType = "kill_encoder"
	ActionReconnect        ActionType = "reconnect"
	ActionAVDesync         ActionType = "av_desync"
	ActionPTSJump          ActionType = "pts_jump"
	ActionKeyframeMisalign ActionType = "keyframe_misalign"
)

// Event is one timeline entry. Exactly one of Network/Action/SourceSwitch is set
// (enforced by validate.go).
type Event struct {
	At           Duration     `yaml:"at"`
	Network      *NetworkSpec `yaml:"network"`
	Action       ActionType   `yaml:"action"`
	Params       ActionParams `yaml:"params"`
	SourceSwitch *Source      `yaml:"source_switch"`
}

// ActionParams carries the per-action tunables (offset/jump/duration).
type ActionParams struct {
	Offset   *Duration `yaml:"offset"`   // av_desync
	Jump     *Duration `yaml:"jump"`     // pts_jump
	Duration *Duration `yaml:"duration"` // kill_encoder / reconnect (stay offline N seconds)
}

// NetworkSpec is one of: the literal string "clear" (remove all shaping), the
// literal string "cut" (a full-link blackout — see Cut), or an object of
// impairment fields. Clear/Cut are represented by their bool flags.
type NetworkSpec struct {
	Clear bool

	// Cut is a bidirectional blackout: it drops packets in BOTH directions on the
	// uplink, freezing the connection instead of tearing it down. Unlike
	// `loss: 100%` (egress only, so the peer's connection-close RST still reaches
	// the encoder and kills it), a cut lets the same session resume on `clear` —
	// the faithful model of a broadcaster temporarily losing internet.
	Cut bool

	Delay     *Duration `yaml:"delay"`
	Jitter    *Duration `yaml:"jitter"`
	Loss      *Percent  `yaml:"loss"`
	Corrupt   *Percent  `yaml:"corrupt"`
	Duplicate *Percent  `yaml:"duplicate"`
	Reorder   *Percent  `yaml:"reorder"`
	Rate      *Bitrate  `yaml:"rate"`
	Accurate  bool      `yaml:"accurate"` // stack htb/tbf under netem for a truthful rate cap
}

// UnmarshalYAML accepts the scalars `network: clear` / `network: cut` and the
// object form `network: { ... }`.
func (n *NetworkSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		switch s {
		case "clear":
			*n = NetworkSpec{Clear: true}
		case "cut":
			*n = NetworkSpec{Cut: true}
		default:
			return fmt.Errorf("network scalar must be \"clear\" or \"cut\", got %q", s)
		}
		return nil
	}
	// Object form: decode into an alias to avoid recursion.
	type raw NetworkSpec
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*n = NetworkSpec(r)
	return nil
}

// SCTE describes SCTE-35 marker authoring (requires threefive).
type SCTE struct {
	Enabled       bool     `yaml:"enabled"`
	Type          string   `yaml:"type"` // time_signal | splice_insert
	Cadence       Duration `yaml:"cadence"`
	Preroll       Duration `yaml:"preroll"`
	BreakDuration Duration `yaml:"break_duration"`
	Misalign      bool     `yaml:"misalign"`
}

// Ad optionally splices a real ad segment at each SCTE break with an
// independently configurable profile to reproduce boundary quality steps.
type Ad struct {
	Enabled  bool     `yaml:"enabled"`
	Source   Source   `yaml:"source"`
	Encoder  Encoder  `yaml:"encoder"`
	Duration Duration `yaml:"duration"`
}

// Verify configures the player + verifier stage.
type Verify struct {
	Enabled       bool     `yaml:"enabled"`
	Pull          string   `yaml:"pull"`
	DegradePlayer bool     `yaml:"degrade_player"`
	Checks        []string `yaml:"checks"`
	Report        string   `yaml:"report"`
}

// Load reads, parses, sorts the timeline, and validates a scenario file.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes into a Scenario, sorts the timeline, and validates.
func Parse(data []byte) (*Scenario, error) {
	var s Scenario
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown keys → catches typos in scenarios
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse scenario yaml: %w", err)
	}
	s.sortTimeline()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Scenario) sortTimeline() {
	sort.SliceStable(s.Timeline, func(i, j int) bool {
		return s.Timeline[i].At < s.Timeline[j].At
	})
}
