// Package apidocs embeds the API contract shipped with this executable.
package apidocs

import _ "embed"

// OpenAPI is the same contract published in the repository.
//
//go:embed openapi-v2.yaml
var OpenAPI []byte
