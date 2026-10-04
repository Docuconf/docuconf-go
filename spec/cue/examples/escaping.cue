package examples

import "docuconf.dev/contract"

// Kubernetes expands $(VAR) in env values and turns $$ into $, so a
// literal is only delivered intact if every $ is doubled.
escaping: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "escaping", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		GREETING_TEMPLATE: {type: "string", description: "Template with literal dollar signs"}
		PRICE_REGEX: {type: "string", description: "Regex anchored with a dollar sign"}
	}
}

escapingEnv: (contract.#Render & {contract: escaping, values: {
	GREETING_TEMPLATE: "Hi $(HOSTNAME), you owe $$5"
	PRICE_REGEX:       "^[0-9]+$"
}}).env
