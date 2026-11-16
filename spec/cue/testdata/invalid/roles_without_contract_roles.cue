package examples

import "docuconf.dev/contract"

// want: _inputRoles
// Inputs have roles only when the contract declares them.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: PORT: {type: "int", description: "Listen port", roles: ["web"]}
}
