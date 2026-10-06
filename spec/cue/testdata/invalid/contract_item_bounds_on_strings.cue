package examples

import "docuconf.dev/contract"

// want: _itemBoundsOnIntItems
// itemMin and itemMax only apply to int lists.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "bad", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: TAGS: {type: "list", description: "Tags to apply", items: "string", itemMax: 3}
}
