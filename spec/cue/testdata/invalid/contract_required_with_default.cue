package examples

import "docuconf.dev/contract"

// want: vars.PORT
// Required var cannot have a default.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: PORT: {type: "int", description: "HTTP listen port", required: true, default: 8080}
}
