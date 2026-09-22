//go:build tools

// This file pins the libraries aval's milestones will use, so go.mod stays
// stable while several branches are developed in parallel. It is never built:
// the "tools" tag only exists to keep these modules in go.mod and go.sum.
// Remove an import once real code depends on the module.
package main

import (
	_ "charm.land/bubbles/v2/spinner"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/glamour/v2"
	_ "charm.land/huh/v2"
	_ "github.com/getkin/kin-openapi/openapi3"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "go.uber.org/goleak"
	_ "go.yaml.in/yaml/v3"
	_ "golang.org/x/sync/errgroup"
	_ "golang.org/x/tools/go/packages"
	_ "pgregory.net/rapid"
)
