// Command sdkgen writes the generated parts of the SDKs that are not built
// by their own tooling: the Python SDK's operations module. Run it from the
// repository root after changing api/openapi.yaml:
//
//	go run ./engine/cmd/sdkgen
package main

import (
	"fmt"
	"os"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/cli"
)

const pythonOut = "sdk/python/src/outflare/_operations.py"

func main() {
	src, err := cli.GeneratePython(spec.OpenAPI)
	if err == nil {
		err = os.WriteFile(pythonOut, src, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkgen:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", pythonOut)
}
