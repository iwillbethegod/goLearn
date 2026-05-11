//go:build tools

// Package tools pins build-time tool dependencies in go.mod so that
// every contributor regenerates code with the same versions. The
// `tools` build tag means none of these imports end up in any
// production binary.
//
// Note: protoc itself is NOT a Go tool — install it via the OS
// package manager (`brew install protobuf` on macOS,
// `apt-get install -y protobuf-compiler` on Debian/Ubuntu). Only
// the protoc *plugins* are pinned here.
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)
