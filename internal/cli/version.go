package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
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
			out := cmd.OutOrStdout()
			if g.json {
				return writeEnvelope(out, "version", info)
			}
			line := "aval " + info.Version
			if info.Commit != "" {
				line += " (" + info.Commit + ")"
			}
			if _, err := fmt.Fprintln(out, line+" "+info.Go); err != nil {
				return fmt.Errorf("write version: %w", err)
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
