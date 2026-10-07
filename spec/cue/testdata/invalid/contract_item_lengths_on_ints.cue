package examples

import "docuconf.dev/contract"

// want: _itemLengthsOnStringItems
// itemMinLength and itemMaxLength only apply to string lists.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "bad", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: PORTS: {type: "list", description: "Ports to open", items: "int", itemMaxLength: 5}
}
