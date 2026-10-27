// Command outflare runs the Outflare engine (the HTTP API and the job
// workers), and is its command line client.
//
//	outflare serve     run migrations, then the API and workers
//	outflare migrate   run migrations and exit
//	outflare version   print the version
//	outflare login     save a server URL and API token for the client
//	outflare help      every API operation, as a command
package main

import "github.com/outflareai/outflare/engine/outflare"

func main() { outflare.Main() }
