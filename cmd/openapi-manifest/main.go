// openapi-manifest regenerates the client operation map from shipped OpenAPI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"dont/internal/apicontract"
)

func main() {
	out := flag.String("out", "docs/openapi-operations.json", "output path")
	check := flag.Bool("check", false, "check that the generated mapping is current")
	flag.Parse()
	data, err := apicontract.Manifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check {
		current, err := os.ReadFile(*out)
		if err != nil || !bytes.Equal(data, current) {
			fmt.Fprintln(os.Stderr, "OpenAPI client mapping is stale:", *out)
			os.Exit(1)
		}
		return
	}
	if err = os.MkdirAll(filepath.Dir(*out), 0755); err == nil {
		err = os.WriteFile(*out, data, 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
