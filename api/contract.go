// SPDX-License-Identifier: Apache-2.0

// Package api holds Araldo's HTTP API contract, openapi.yaml (ADR 0005).
// Like the SDKs generated from it, the contract is Apache-2.0 licensed
// (ADR 0003).
package api

import _ "embed"

// OpenAPI is the contract, served at /v1/openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
