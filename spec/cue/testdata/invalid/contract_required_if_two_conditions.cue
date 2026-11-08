package examples

import "docuconf.dev/contract"

// want: requiredIf
// One variable only: richer logic belongs in the app.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		A: {type: "bool", description: "Flag one"}
		B: {type: "bool", description: "Flag two"}
		C: {type: "string", description: "Needs both", requiredIf: {A: true, B: true}}
	}
}
