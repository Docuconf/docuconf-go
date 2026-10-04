package examples

import "docuconf.dev/contract"

// want: vars.port: field not allowed
// Env names are UPPER_SNAKE_CASE.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: port: {type: "int", description: "HTTP listen port"}
}
