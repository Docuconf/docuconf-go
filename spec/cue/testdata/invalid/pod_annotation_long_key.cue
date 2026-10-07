package examples

import "docuconf.dev/contract"

// want: podAnnotationKey
// Expanded, the key's name part is longer than Kubernetes allows (63).
bad: contract.#Validate & {
	contract: ledger
	files: "db-creds": injected: {
		provider: "vault-agent"
		podAnnotations: "vault.hashicorp.com/agent-inject-secret-{input}-{file}-with-a-suffix-long-enough-to-overflow": "kv/ledger"
	}
}
