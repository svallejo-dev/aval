package cli

import (
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridden at release time with
// -ldflags "-X github.com/svallejo-dev/aval/internal/cli.version=v0.1.0".
var version = ""

// versionInfo is the data payload of `aval version --json`.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Go      string `json:"go"`
}

// envelope is the JSON contract every command emits with --json (ADR-0004).
type envelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Command       string          `json:"command"`
	OK            bool            `json:"ok"`
	Data          any             `json:"data"`
	Errors        []envelopeError `json:"errors"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func newVersionCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print aval's version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := readVersion()
			out := cmd.OutOrStdout()
			if g.json {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				e := envelope{SchemaVersion: 1, Command: "version", OK: true, Data: info, Errors: []envelopeError{}}
				if err := enc.Encode(e); err != nil {
					return fmt.Errorf("encode version: %w", err)
				}
				return nil
			}
			line := "aval " + info.Version
			if info.Commit != "" {
				line += " (" + info.Commit + ")"
			}
			if _, err := fmt.Fprintln(out, line+" "+info.Go); err != nil {
				return fmt.Errorf("write version: %w", err)
			}
			return nil
		},
	}
}

// readVersion prefers the release ldflag and falls back to the module
// version and VCS revision that `go build` / `go install` stamp into the binary.
func readVersion() versionInfo {
	info := versionInfo{Version: "devel", Go: "unknown"}
	bi, ok := debug.ReadBuildInfo()
	if ok {
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
