// Package manifest defines aval's YAML manifests: the repository manifest
// (aval.yaml at the repository root) and the per-change manifest
// (openspec/changes/<id>/aval.yaml). Both are decoded strictly: an unknown
// field is an error, so a typo can never silently weaken the policy.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"
)

// Version is the manifest schema version this build understands.
const Version = 1

// Mode decides whether the gate blocks.
type Mode string

// Gate modes.
const (
	Observe Mode = "observe" // report the verdict, never block
	Enforce Mode = "enforce" // block when the verdict is not a pass
)

// Tier is the risk tier of a change, from 0 (no change artifacts) to 3.
type Tier int

// Valid reports whether t is between 0 and 3.
func (t Tier) Valid() bool { return t >= 0 && t <= 3 }

// Repo is the repository manifest, aval.yaml.
type Repo struct {
	Version     int      `yaml:"version" json:"version"`
	Context     string   `yaml:"context" json:"context"`
	Mode        Mode     `yaml:"mode" json:"mode"`
	TierDefault Tier     `yaml:"tierDefault" json:"tierDefault"`
	OpenSpec    OpenSpec `yaml:"openspec" json:"openspec"`
	Paths       Paths    `yaml:"paths" json:"paths"`
}

// OpenSpec pins the OpenSpec CLI version aval validates specs with.
type OpenSpec struct {
	Version string `yaml:"version" json:"version"`
}

// Paths assigns repository paths to a command family, as doublestar globs.
// A commit may only touch paths of one family; seam paths are shared.
type Paths struct {
	DX   []string `yaml:"dx" json:"dx"`
	Feat []string `yaml:"feat" json:"feat"`
	Seam []string `yaml:"seam,omitempty" json:"seam,omitempty"`
}

// Change is the per-change manifest, openspec/changes/<id>/aval.yaml.
type Change struct {
	Version int    `yaml:"version" json:"version"`
	Tier    Tier   `yaml:"tier" json:"tier"`
	Owner   string `yaml:"owner,omitempty" json:"owner,omitempty"`
}

// ErrInvalid is wrapped by every validation error.
var ErrInvalid = errors.New("invalid manifest")

var (
	contextRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
	semverRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// ParseRepo decodes and validates a repository manifest.
func ParseRepo(r io.Reader) (Repo, error) {
	var m Repo
	if err := decodeStrict(r, &m); err != nil {
		return Repo{}, fmt.Errorf("aval.yaml: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Repo{}, fmt.Errorf("aval.yaml: %w", err)
	}
	return m, nil
}

// ParseChange decodes and validates a per-change manifest.
func ParseChange(r io.Reader) (Change, error) {
	var c Change
	if err := decodeStrict(r, &c); err != nil {
		return Change{}, fmt.Errorf("change manifest: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Change{}, fmt.Errorf("change manifest: %w", err)
	}
	return c, nil
}

// Validate reports every problem in m, joined into one error.
func (m Repo) Validate() error {
	var errs []error
	if m.Version != Version {
		errs = append(errs, fmt.Errorf("version: got %d, want %d", m.Version, Version))
	}
	if !contextRe.MatchString(m.Context) {
		errs = append(errs, fmt.Errorf("context: %q must be 2-10 upper-case letters or digits, starting with a letter", m.Context))
	}
	if m.Mode != Observe && m.Mode != Enforce {
		errs = append(errs, fmt.Errorf("mode: %q must be %q or %q", m.Mode, Observe, Enforce))
	}
	if !m.TierDefault.Valid() {
		errs = append(errs, fmt.Errorf("tierDefault: %d must be between 0 and 3", m.TierDefault))
	}
	if !semverRe.MatchString(m.OpenSpec.Version) {
		errs = append(errs, fmt.Errorf("openspec.version: %q must be an exact version like 1.13.1", m.OpenSpec.Version))
	}
	errs = append(errs, m.Paths.validate()...)
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return nil
}

func (p Paths) validate() []error {
	var errs []error
	if len(p.DX) == 0 {
		errs = append(errs, errors.New("paths.dx: at least one pattern is required"))
	}
	if len(p.Feat) == 0 {
		errs = append(errs, errors.New("paths.feat: at least one pattern is required"))
	}
	families := []struct {
		name     string
		patterns []string
	}{{"dx", p.DX}, {"feat", p.Feat}, {"seam", p.Seam}}

	seen := make(map[string]string)
	for _, f := range families {
		for _, pat := range f.patterns {
			if !doublestar.ValidatePattern(pat) {
				errs = append(errs, fmt.Errorf("paths.%s: %q is not a valid glob", f.name, pat))
			}
			if other, dup := seen[pat]; dup {
				errs = append(errs, fmt.Errorf("paths: %q appears in both %s and %s", pat, other, f.name))
			}
			seen[pat] = f.name
		}
	}
	return errs
}

// Validate reports every problem in c, joined into one error.
func (c Change) Validate() error {
	var errs []error
	if c.Version != Version {
		errs = append(errs, fmt.Errorf("version: got %d, want %d", c.Version, Version))
	}
	if !c.Tier.Valid() {
		errs = append(errs, fmt.Errorf("tier: %d must be between 0 and 3", c.Tier))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return nil
}

// decodeStrict decodes a single YAML document, rejecting unknown fields,
// empty input and trailing documents.
func decodeStrict(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%w: empty document", ErrInvalid)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: exactly one YAML document is allowed", ErrInvalid)
	}
	return nil
}
