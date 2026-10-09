package examples

import "docuconf.dev/contract"

// want: vars.OLD_PORT.deprecated.message
// A deprecation says what to use instead, or why the input is going away.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: OLD_PORT: {type: "int", description: "Old listen port", deprecated: message: " "}
}
