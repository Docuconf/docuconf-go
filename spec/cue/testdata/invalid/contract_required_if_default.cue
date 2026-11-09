package examples

import "docuconf.dev/contract"

// want: vars.C
// A conditionally required variable has no default.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		A: {type: "bool", description: "Flag one"}
		C: {type: "string", description: "Needs A", default: "x", requiredIf: A: true}
	}
}
