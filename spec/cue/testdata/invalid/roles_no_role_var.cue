package examples

import "docuconf.dev/contract"

// want: _roleVariable
// A contract with roles declares DOCUCONF_ROLE.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	roles: ["web", "worker"]
	vars: PORT: {type: "int", description: "Listen port"}
}
