package examples

import "docuconf.dev/contract"

// want: _inputRoles
// An input names only declared roles.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	roles: ["web", "worker"]
	vars: {
		DOCUCONF_ROLE: {type: "enum", description: "The workload role", required: true, values: ["web", "worker"]}
		PORT: {type: "int", description: "Listen port", roles: ["api"]}
	}
}
