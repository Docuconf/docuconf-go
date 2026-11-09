package examples

import "docuconf.dev/contract"

// want: conditionIsPlain
// A secret has no value before deploy to compare.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		TOKEN: {type: "string", description: "API token", secret: true}
		TOKEN_ISSUER: {type: "string", description: "Token issuer", requiredIf: TOKEN: "x"}
	}
}
