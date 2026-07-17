package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	sw "streamwreck"
	"streamwreck/internal/controller"
	"streamwreck/internal/report"
	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
)

// defaultComposeFile locates deploy/docker-compose.yml relative to the binary's
// working dir, falling back to the conventional repo path.
func defaultComposeFile() string {
	if _, err := os.Stat("deploy/docker-compose.yml"); err == nil {
		return "deploy/docker-compose.yml"
	}
	return filepath.Join("deploy", "docker-compose.yml")
}

func newRunner(cmd *cobra.Command) run.Runner {
	compose, _ := cmd.Flags().GetString("compose")
	project, _ := cmd.Flags().GetString("project")
	var profiles []string
	if lab, _ := cmd.Flags().GetBool("lab"); lab {
		profiles = append(profiles, "lab")
	}
	return run.NewDocker(compose, project, profiles...)
}

// signalCtx returns a context cancelled on SIGINT/SIGTERM so a run tears down
// cleanly on Ctrl-C.
func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// userPresetDir is where `init` writes user-created scenarios (gitignored). A
// --preset name is resolved from the embedded bundle first, then from here.
const userPresetDir = "presets/user"

// loadPreset resolves a --preset name: bundled (embedded) presets first, then a
// user preset on disk under presets/user/<name>.yaml.
func loadPreset(name string) ([]byte, error) {
	if data, err := sw.Preset(name); err == nil {
		return data, nil
	}
	if data, err := os.ReadFile(filepath.Join(userPresetDir, name+".yaml")); err == nil {
		return data, nil
	}
	return nil, fmt.Errorf("unknown preset %q (see `streamwreck presets`)", name)
}

// loadScenario resolves a scenario from either a --preset name or a file path,
// then applies any --source-file override.
func loadScenario(cmd *cobra.Command, args []string) (*scenario.Scenario, error) {
	var (
		s   *scenario.Scenario
		err error
	)
	preset, _ := cmd.Flags().GetString("preset")
	switch {
	case preset != "":
		data, perr := loadPreset(preset)
		if perr != nil {
			return nil, perr
		}
		s, err = scenario.Parse(data)
	case len(args) > 0:
		s, err = scenario.Load(args[0])
	default:
		return nil, fmt.Errorf("provide a scenario file or --preset <name>")
	}
	if err != nil {
		return nil, err
	}
	if sf, _ := cmd.Flags().GetString("source-file"); sf != "" {
		applySourceFile(s, sf)
		// Re-validate: the override changes the source shape.
		if verr := s.Validate(); verr != nil {
			return nil, verr
		}
	}
	if ds, _ := cmd.Flags().GetString("duration"); ds != "" {
		d, derr := time.ParseDuration(ds)
		if derr != nil || d <= 0 {
			return nil, fmt.Errorf("invalid --duration %q: use a positive duration like 90s or 10m", ds)
		}
		sd := scenario.Duration(d)
		s.RunDuration = &sd
	}
	return s, nil
}

// applySourceFile overrides the scenario's source with a file, preserving the
// original resolution/fps/timecode settings. A bare or relative path is resolved
// under /media (the encoder's read-only media mount); an absolute path is used
// as the encoder sees it.
func applySourceFile(s *scenario.Scenario, path string) {
	if !strings.HasPrefix(path, "/") {
		path = "/media/" + path
	}
	s.Source.Type = scenario.SourceFile
	s.Source.File = path
	s.Source.Complexity = "" // irrelevant for a file source
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [scenario.yaml]",
		Short: "Execute a scenario end to end",
		Long:  "Execute a scenario end to end. Exits non-zero if any enabled verification check fails.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadScenario(cmd, args)
			if err != nil {
				return err
			}
			ctx, cancel := signalCtx()
			defer cancel()

			c := controller.New(newRunner(cmd))
			rep, err := c.Run(ctx, s)
			if err != nil {
				return err
			}
			if rep == nil {
				return nil
			}
			rep.Print(os.Stdout)
			if !rep.Pass {
				// Non-zero exit makes `run` usable as a CI gate.
				return fmt.Errorf("verification failed: %s", rep.Summary())
			}
			return nil
		},
	}
	cmd.Flags().String("preset", "", "run a bundled preset by name instead of a file")
	cmd.Flags().String("source-file", "", "override the scenario source with this video file "+
		"(path under /media, e.g. myclip.mp4 or /media/sub/clip.mp4)")
	cmd.Flags().String("duration", "", "how long the stream runs (e.g. 90s, 10m); "+
		"overrides the scenario's duration and the timeline-derived default")
	return cmd
}

func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [scenario.yaml]",
		Short: "Lint/typecheck a scenario without executing it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := loadScenario(cmd, args)
			if err != nil {
				return err
			}
			if _, note := scenario.NormalizeIngestURL(s.Output.URL); note != "" {
				fmt.Printf("note: %s (streamwreck will apply this automatically at run time)\n", note)
			}
			fmt.Printf("ok: %q is valid (%d timeline events)\n", s.Name, len(s.Timeline))
			return nil
		},
	}
	cmd.Flags().String("preset", "", "validate a bundled preset by name")
	cmd.Flags().String("source-file", "", "override the scenario source with this video file "+
		"(path under /media)")
	return cmd
}

func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a scenario pointed at your real ingest (and optional playback URL)",
		Long: "Interactively (or via flags) generate a scenario that publishes to your real\n" +
			"ingest and, optionally, verifies a playback URL. Point output.url at your\n" +
			"platform's ingest with the stream key; add a playback URL to grade viewer QoE.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			in := scaffoldInput{}
			in.Ingest, _ = cmd.Flags().GetString("ingest")
			in.Pull, _ = cmd.Flags().GetString("pull")
			in.Protocol, _ = cmd.Flags().GetString("protocol")
			in.Profile, _ = cmd.Flags().GetString("profile")
			in.SourceFile, _ = cmd.Flags().GetString("source-file")
			in.Name, _ = cmd.Flags().GetString("name")
			out, _ := cmd.Flags().GetString("output")
			force, _ := cmd.Flags().GetBool("force")

			// Interactive unless --ingest was supplied (which enables scripting).
			if in.Ingest == "" {
				r := bufio.NewReader(cmd.InOrStdin())
				in = gatherInteractive(in, r, cmd.OutOrStdout())
			}
			if in.Protocol == "" {
				in.Protocol = defaultProtocol(in.Ingest)
			}
			if in.Profile == "" {
				in.Profile = "flaky-uplink"
			}
			in.Name = slugify(in.Name)
			if in.Name == "" {
				in.Name = "mytest"
			}
			// Default location: presets/user/<name>.yaml (gitignored). An explicit
			// --output is honored as-is.
			if out == "" {
				out = filepath.Join(userPresetDir, in.Name+".yaml")
			}

			// Repair known ingest-URL mistakes (e.g. IVS :443//app/) up front so
			// the generated scenario is correct and runnable as written.
			if fixed, note := scenario.NormalizeIngestURL(in.Ingest); note != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", note)
				in.Ingest = fixed
			}

			yaml, err := scaffoldYAML(in)
			if err != nil {
				return err
			}
			if !force {
				if _, serr := os.Stat(out); serr == nil {
					return fmt.Errorf("%s already exists (use --force to overwrite)", out)
				}
			}
			if derr := os.MkdirAll(filepath.Dir(out), 0o755); derr != nil {
				return derr
			}
			if werr := os.WriteFile(out, []byte(yaml), 0o644); werr != nil {
				return werr
			}
			fmt.Fprintf(os.Stderr, "\nwrote %s\n", out)
			fmt.Fprintf(os.Stderr, "next:\n")
			fmt.Fprintf(os.Stderr, "  1. review output.url — confirm the stream key, use a staging channel\n")
			fmt.Fprintf(os.Stderr, "  2. streamwreck up\n")
			// User presets are resolvable by name (loadPreset checks presets/user).
			if strings.HasPrefix(filepath.ToSlash(out), userPresetDir+"/") {
				fmt.Fprintf(os.Stderr, "  3. streamwreck run --preset %s\n", in.Name)
			} else {
				fmt.Fprintf(os.Stderr, "  3. streamwreck run %s\n", out)
			}
			return nil
		},
	}
	cmd.Flags().String("ingest", "", "ingest URL with stream key (sets output.url; enables non-interactive mode)")
	cmd.Flags().String("pull", "", "playback URL to verify (sets verify.pull); optional")
	cmd.Flags().String("protocol", "", "rtmp|srt (default: inferred from the ingest URL)")
	cmd.Flags().String("profile", "", "impairment profile: clean|flaky-uplink|reconnect (default: flaky-uplink)")
	cmd.Flags().String("source-file", "", "stream your own video from /media instead of a test pattern")
	cmd.Flags().String("name", "", "scenario name (default: derived from the ingest host)")
	cmd.Flags().StringP("output", "o", "", "file to write (default: <name>.yaml)")
	cmd.Flags().Bool("force", false, "overwrite an existing output file")
	return cmd
}

// slugify turns a user-supplied name into a lowercase, filename-safe slug
// (alphanumerics kept, everything else collapsed to single dashes).
func slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func presetsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "presets",
		Short: "List bundled and user presets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printPreset := func(name string, data []byte) {
				desc := ""
				if s, perr := scenario.Parse(data); perr == nil {
					desc = s.Description
				}
				fmt.Printf("  %-24s %s\n", name, desc)
			}
			fmt.Println("bundled:")
			for _, name := range sw.PresetNames() {
				if data, err := sw.Preset(name); err == nil {
					printPreset(name, data)
				}
			}
			// User presets from presets/user/ (created by `init`).
			if entries, _ := os.ReadDir(userPresetDir); len(entries) > 0 {
				fmt.Printf("\nuser (%s):\n", userPresetDir)
				for _, e := range entries {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
						continue
					}
					name := strings.TrimSuffix(e.Name(), ".yaml")
					if data, err := os.ReadFile(filepath.Join(userPresetDir, e.Name())); err == nil {
						printPreset(name, data)
					}
				}
			}
			return nil
		},
	}
}

func reportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report <report.json>",
		Short: "Pretty-print a verification report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := report.Load(args[0])
			if err != nil {
				return err
			}
			rep.Print(os.Stdout)
			if !rep.Pass {
				return fmt.Errorf("report shows failures")
			}
			return nil
		},
	}
}

func upCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the testing machinery (add --lab for the bundled demo origin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalCtx()
			defer cancel()
			return newRunner(cmd).ComposeUp(ctx)
		},
	}
	cmd.Flags().Bool("lab", false, "also start the bundled MediaMTX demo origin (for trying it without your own platform)")
	return cmd
}

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop and remove the stack (including the demo origin, if running)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalCtx()
			defer cancel()
			// Always tear down comprehensively — include the lab profile so the
			// demo origin is removed even when `down` is called without --lab.
			compose, _ := cmd.Flags().GetString("compose")
			project, _ := cmd.Flags().GetString("project")
			return run.NewDocker(compose, project, "lab").ComposeDown(ctx)
		},
	}
}
