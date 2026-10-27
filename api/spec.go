// Package api embeds the OpenAPI document, the contract that the SDKs, the MCP
// server and the docs are generated from.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
