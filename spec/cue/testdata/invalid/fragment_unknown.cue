package examples

import "docuconf.dev/contract"

// want: includes
// Only well-known fragments can be included.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	includes: ["kafka@1"]
	vars: {}
}
