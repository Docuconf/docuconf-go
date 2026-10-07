package examples

import "docuconf.dev/contract"

// want: podAnnotations."vault.hashicorp.com/agent-inject": conflicting values
// Annotation values are strings; YAML's unquoted true is a boolean.
bad: contract.#Validate & {
	contract: ledger
	values: podAnnotations: "vault.hashicorp.com/agent-inject": true
}
