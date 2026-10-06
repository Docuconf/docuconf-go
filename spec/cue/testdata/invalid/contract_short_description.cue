package examples

import "docuconf.dev/contract"

// want: vars.PORT
// Description must be at least 5 characters.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: PORT: {type: "int", description: "port"}
}
