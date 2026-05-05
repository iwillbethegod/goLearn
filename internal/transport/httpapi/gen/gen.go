// Package gen holds the oapi-codegen-generated server interface and
// types. The single source of truth is api/openapi.yaml. To regenerate
// after editing the spec, run:
//
//	go generate ./...
package gen

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=cfg.yaml ../../../../api/openapi.yaml
