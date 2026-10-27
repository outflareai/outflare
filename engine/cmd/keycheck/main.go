// Command keycheck enforces the bring-your-own-keys rule (AGENTS.md) on a
// source tree: it fails when code reads a third-party key from the
// environment, unless the allowlist names that file for Outflare's own
// operations. The engine's own test runs the same check; Cloud runs
//
//	go run github.com/outflareai/outflare/engine/cmd/keycheck -allow security/platform-credentials.txt .
//
// With no -allow, it uses the engine's allowlist.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/outflareai/outflare/engine/internal/ownkeys"
)

func main() {
	allow := flag.String("allow", "", "allowlist file (default: the engine's)")
	flag.Parse()
	root := flag.Arg(0)
	if root == "" {
		root = "."
	}
	rules := ownkeys.Allowed()
	if *allow != "" {
		b, err := os.ReadFile(*allow)
		if err == nil {
			rules, err = ownkeys.ParseRules(b)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "keycheck:", err)
			os.Exit(2)
		}
	}
	found, err := ownkeys.Scan(root, rules, ownkeys.Vendors())
	if err != nil {
		fmt.Fprintln(os.Stderr, "keycheck:", err)
		os.Exit(2)
	}
	for _, f := range found {
		fmt.Println(f)
	}
	if len(found) > 0 {
		fmt.Fprintf(os.Stderr, "\nkeycheck: %d place(s) could use a key that isn't the workspace's own.\n"+
			"Workspaces bring their own keys for every third-party API, with no exceptions (AGENTS.md, \"Bring your own keys\").\n"+
			"Take the key from the workspace's connection instead. Only Outflare's own operations go in the allowlist, with a maintainer's approval.\n", len(found))
		os.Exit(1)
	}
}
