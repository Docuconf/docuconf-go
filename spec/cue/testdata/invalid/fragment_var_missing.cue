package examples

import "docuconf.dev/contract"

// want: _fragmentVarDeclared
// An included fragment's variables are inline in vars.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	includes: ["go-runtime@1"]
	vars: GOMAXPROCS: {type: "int", description: "Max threads running Go code"}
}
