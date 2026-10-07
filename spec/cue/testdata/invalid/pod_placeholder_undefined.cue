package examples

import "docuconf.dev/contract"

// want: undefinedPlaceholder
// {path} is only defined for a file input, not for a variable.
bad: contract.#Validate & {
	contract: ledger
	values: DB_PASSWORD: injected: {
		provider: "vault-agent"
		podAnnotations: "vault.hashicorp.com/agent-inject-file-{input}": "{path}"
	}
}
