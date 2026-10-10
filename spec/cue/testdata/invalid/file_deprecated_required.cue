package examples

import "docuconf.dev/contract"

// want: _aRequiredInputCannotBeDeprecated
// A required file input cannot be deprecated either.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {}
	files: licence: {type: "text", description: "Licence key file", path: "/etc/x/licence.txt", required: true, deprecated: message: "Licences are checked online"}
}
