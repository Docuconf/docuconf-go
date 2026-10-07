package examples

import (
	"strings"

	"docuconf.dev/contract"
)

// want: does not satisfy strings.MaxRunes(4000)
// details is at most 4000 characters.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {}
	files: routes: {
		type:        "config"
		format:      "yaml"
		description: "Routing table"
		path:        "/etc/gw/routes.yaml"
		details:     strings.Repeat("x", 4001)
	}
}
