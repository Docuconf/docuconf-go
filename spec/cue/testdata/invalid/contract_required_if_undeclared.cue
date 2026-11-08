package examples

import "docuconf.dev/contract"

// want: conditionIsDeclared
// A condition must name a declared variable.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: CERT: {type: "string", description: "Certificate path", requiredIf: TLS_ENABLED: true}
}
