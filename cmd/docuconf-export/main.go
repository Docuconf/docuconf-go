// Command docuconf-export writes the docuconf contract for a Go
// configuration struct. It is "docuconf export" without the rest of the
// docuconf CLI: it lives in the SDK's own module, needs only the standard
// library and Go 1.24, and so adds nothing to an app's go.mod when pinned
// as a tool:
//
//	go get -tool github.com/docuconf/docuconf-go/cmd/docuconf-export
//	go tool docuconf-export -pkg ./internal/config -name billing-api -o contract.cue
//	go tool docuconf-export -pkg ./internal/config -name billing-api -check contract.cue
//
// or from a go:generate line:
//
//	//go:generate go run github.com/docuconf/docuconf-go/cmd/docuconf-export -pkg . -name billing-api -o ../../contract.cue
//
// It exits 0 on success, 1 when -check finds the contract out of date,
// and 2 on any other error. The platform side (vet, render, helm) is in
// the docuconf command, github.com/docuconf/docuconf-go/cmd/docuconf.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	err := runExport(args, stdout, stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errStale):
		return 1
	}
	fmt.Fprintf(stderr, "docuconf-export: %v\n", err)
	return 2
}
