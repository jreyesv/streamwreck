// Package streamwreck (module root) embeds the bundled preset scenarios so the
// binary is self-contained: `streamwreck presets` and `run --preset` resolve
// from here without needing the source tree on disk.
package streamwreck

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed presets/*.yaml
var presetFS embed.FS

// PresetNames returns the bundled preset names (without directory or extension),
// sorted.
func PresetNames() []string {
	entries, _ := fs.ReadDir(presetFS, "presets")
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names
}

// Preset returns the raw YAML bytes for a bundled preset by name.
func Preset(name string) ([]byte, error) {
	return presetFS.ReadFile("presets/" + name + ".yaml")
}
