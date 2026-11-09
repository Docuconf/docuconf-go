package examples

import "docuconf.dev/contract"

// want: conditionValue
// The value must be one the condition variable accepts.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		TLS_ENABLED: {type: "bool", description: "Serve HTTPS"}
		CERT: {type: "string", description: "Certificate path", requiredIf: TLS_ENABLED: "yes"}
	}
}
