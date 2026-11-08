// Command function-docuconf is a Crossplane composition function: it pulls
// the contract of the image a composite resource deploys, checks the
// platform's values against it with the environment's policy, sets the
// ContractValid condition, and renders the values into the composed
// workload (docs/PLAN.md, Phase 2; README.md).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/crossplane/function-sdk-go"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
)

func main() {
	debug := flag.Bool("debug", false, "log debug messages")
	address := flag.String("address", ":9443", "the address to listen on")
	certs := flag.String("tls-server-certs-dir", os.Getenv("TLS_SERVER_CERTS_DIR"), "the directory of the server's mTLS certificates (tls.key, tls.crt, ca.crt)")
	insecure := flag.Bool("insecure", false, "serve without mTLS (crossplane render, local development)")
	flag.Parse()

	log, err := function.NewLogger(*debug)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fn, err := NewFunction(log, &oci.Client{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := function.Serve(fn,
		function.Listen("tcp", *address),
		function.MTLSCertificates(*certs),
		function.Insecure(*insecure)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
