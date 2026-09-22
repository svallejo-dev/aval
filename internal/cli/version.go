package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/ui"
)

// version is overridden at release time with
// -ldflags "-X github.com/svallejo-dev/aval/internal/cli.version=v0.1.0".
var version string

// versionInfo is the data payload of `aval version --json`.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Go      string `json:"go"`
}

func newVersionCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print aval's version",
		Args:  cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			info := readVersion()
			details := " " + info.Go
			if info.Commit != "" {
				details = " (" + info.Commit + ")" + details
			}
			err := g.printer(cmd).Print(ui.Result{
				Command: "version",
				Data:    info,
				Lines: []ui.Line{{
					{Text: "aval", Tone: ui.ToneTitle},
					{Text: " " + info.Version},
					{Text: details, Tone: ui.ToneMuted},
				}},
			})
			if err != nil {
				return fmt.Errorf("print version: %w", err)
			}
			return nil
		}),
	}
}

// readVersion prefers the release ldflag and falls back to the module
// version and VCS revision that `go build` / `go install` stamp into the binary.
func readVersion() versionInfo {
	info := versionInfo{Version: "devel", Go: "unknown"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.Go = bi.GoVersion
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			info.Version = v
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				info.Commit = s.Value[:12]
			}
		}
	}
	if version != "" {
		info.Version = version
	}
	return info
}
