// Package api holds Park's HTTP contract: openapi.yaml, from which the server code in internal/httpapi and the
// frontend's client types are generated.
package api

import _ "embed"

// OpenAPI is the contract, as YAML.
//
//go:embed openapi.yaml
var OpenAPI []byte
