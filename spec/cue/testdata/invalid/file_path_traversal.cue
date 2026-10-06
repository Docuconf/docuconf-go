package examples

import "docuconf.dev/contract"

// want: bad.files.x.path: invalid value "/etc/gw/../x.txt"
// Paths must be absolute and normalised.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {}
	files: {x: {type: "text", description: "Some file", path: "/etc/gw/../x.txt"}}
}
