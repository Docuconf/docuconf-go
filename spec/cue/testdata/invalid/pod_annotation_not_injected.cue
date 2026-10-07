package examples

import "docuconf.dev/contract"

// want: podAnnotations: field not allowed
// Only injected sources carry pod metadata.
bad: contract.#Validate & {
	contract: ledger
	values: DB_PASSWORD: {secretKeyRef: {name: "ledger-db", key: "password"}, podAnnotations: "example.com/a": "b"}
}
