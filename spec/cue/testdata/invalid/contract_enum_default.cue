package examples

import "docuconf.dev/contract"

// want: vars.LOG_LEVEL
// Enum default must be one of values.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "EnvContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: LOG_LEVEL: {type: "enum", description: "Log level", values: ["info", "warn"], default: "trace"}
}
