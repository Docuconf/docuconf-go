package examples

import "docuconf.dev/contract"

// want: vars.WEBHOOK_KEYS.itemMinLength
// A key set takes keyMinLength, not the itemMinLength of a list.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: WEBHOOK_KEYS: {type: "keySet", description: "Keys that verify webhooks", secret: true, itemMinLength: 32}
}
