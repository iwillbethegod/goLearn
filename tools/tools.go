//go:build tools

// Package tools pins build-time tool dependencies in go.mod so that
// every contributor regenerates code with the same versions. The
// `tools` build tag means none of these imports end up in any
// production binary.
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
