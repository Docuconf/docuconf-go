package examples

import "docuconf.dev/contract"

// want: _maxKeysAtLeastMinKeys
// maxKeys must be at least minKeys.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: WEBHOOK_KEYS: {type: "keySet", description: "Keys that verify webhooks", secret: true, minKeys: 2, maxKeys: 1}
}
