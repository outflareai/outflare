// Package integrations holds the connectors anyone can add to Outflare
// without writing Go: one folder per connector under connectors/, with a
// manifest, a logo and tests. The engine embeds this folder, so a merged
// manifest ships with the next deploy (and the next release for
// self-hosted installs). See README.md.
package integrations

import "embed"

// FS is publishers.yaml and every connector folder.
//
//go:embed publishers.yaml connectors
var FS embed.FS
