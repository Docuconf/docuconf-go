package examples

import "docuconf.dev/contract"

// want: _requiredIfOnAnOptionalInput
// A required input is always required.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		A: {type: "bool", description: "Flag one"}
		C: {type: "string", description: "Needs A", required: true, requiredIf: A: true}
	}
}
