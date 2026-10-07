package examples

import "docuconf.dev/contract"

// want: conflictingPodAnnotation."vault.hashicorp.com/role"."DB_PASSWORD and db-creds"
// Two injected inputs ask for different values of one pod annotation.
bad: contract.#Validate & {
	contract: ledger
	values: DB_PASSWORD: injected: {provider: "vault-agent", podAnnotations: "vault.hashicorp.com/role": "ledger-ro"}
	files: "db-creds": injected: {provider: "vault-agent", podAnnotations: "vault.hashicorp.com/role": "ledger"}
}
