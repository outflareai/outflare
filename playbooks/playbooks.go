// Package playbooks holds the playbooks anyone can add to Outflare: one
// folder per playbook, each with a playbook.yaml that says what it needs,
// the steps it takes with Outflare's own tools, and the prompt that runs
// it. The engine embeds this folder, so a merged playbook ships with the
// next deploy (and the next release for self-hosted installs). See
// README.md.
package playbooks

import "embed"

// FS is every playbook folder.
//
//go:embed */playbook.yaml
var FS embed.FS
