package examples

import "docuconf.dev/contract"

// want: conditionIsPlain
// Durations do not compare as written (1m and 60s).
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		TIMEOUT: {type: "duration", description: "Request timeout"}
		RETRIES: {type: "int", description: "Retry count", requiredIf: TIMEOUT: "1m"}
	}
}
