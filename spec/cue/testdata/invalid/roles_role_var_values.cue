package examples

import "docuconf.dev/contract"

// want: _roleVariableNamesTheRoles
// DOCUCONF_ROLE allows exactly the declared roles.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	roles: ["web", "worker", "migrate"]
	vars: DOCUCONF_ROLE: {type: "enum", description: "The workload role", required: true, values: ["web", "worker"]}
}
