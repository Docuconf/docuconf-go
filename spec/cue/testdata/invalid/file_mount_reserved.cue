package examples

import "docuconf.dev/contract"

// want: _mountNotReserved
// Mounting at /etc would hide the whole of /etc in the container.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {}
	files: {settings: {type: "config", format: "json", description: "Settings file", path: "/etc/settings.json"}}
}
