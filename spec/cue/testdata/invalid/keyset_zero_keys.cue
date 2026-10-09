package examples

import "docuconf.dev/contract"

// want: _minKeysAtLeastOne
// A key set holds at least one key.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: WEBHOOK_KEYS: {type: "keySet", description: "Keys that verify webhooks", secret: true, minKeys: 0}
}
