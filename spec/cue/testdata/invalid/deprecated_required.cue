package examples

import "docuconf.dev/contract"

// want: _aRequiredInputCannotBeDeprecated
// Deprecating asks the platform to stop setting an input, which a required one cannot do.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: OLD_PORT: {type: "int", description: "Old listen port", required: true, deprecated: message: "Use PORT"}
}
