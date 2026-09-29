// SPDX-License-Identifier: AGPL-3.0-or-later

// Package buildinfo carries the release version into the binary.
//
// Set at build time:
//
//	go build -ldflags "-X github.com/spectrum-labs-tech/araldo/internal/buildinfo.Version=v0.1.0" ./cmd/...
package buildinfo

// Version is the release version, or "dev" for local builds.
var Version = "dev"
