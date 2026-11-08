package examples

import "docuconf.dev/contract"

// want: conditionIsAnotherVariable
// A variable cannot be required on its own value.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: MODE: {type: "string", description: "Serving mode", requiredIf: MODE: "tls"}
}
