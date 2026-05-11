// Package gen carries the //go:generate directive that runs protoc
// over every .proto file in this repo. The actual generated code
// lands in sibling packages (e.g. userpb/) — keeping the directive
// in its own tiny package means a regen never overwrites
// hand-written files.
//
// To regenerate after editing a .proto:
//
//	go generate ./...
//
// The protoc binary itself is not a Go tool; install it once via:
//
//	brew install protobuf            # macOS
//	apt-get install protobuf-compiler # Debian/Ubuntu
//
// The Go plugins (protoc-gen-go, protoc-gen-go-grpc) are pinned in
// tools/tools.go and installed by `go install` of those packages.
package gen

//go:generate protoc -I=../user/v1 --go_out=userpb --go_opt=paths=source_relative --go-grpc_out=userpb --go-grpc_opt=paths=source_relative user.proto
