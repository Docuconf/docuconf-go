package examples

import "docuconf.dev/contract"

// want: _mountDirs
// Two inputs in one directory would hide each other.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {}
	files: {a: {type: "text", description: "First file", path: "/etc/x/a.txt"}
		b: {type: "text", description: "Second file", path: "/etc/x/b.txt"}}
}
