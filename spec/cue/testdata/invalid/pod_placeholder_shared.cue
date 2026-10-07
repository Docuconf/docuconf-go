package examples

import "docuconf.dev/contract"

// want: undefinedPlaceholder
// The shared annotations belong to no input, so {input} means nothing there.
bad: contract.#Validate & {
	contract: ledger
	values: podAnnotations: "vault.hashicorp.com/agent-inject-secret-{input}": "database/creds/ledger"
}
