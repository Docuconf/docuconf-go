package examples

import "docuconf.dev/contract"

// Pod annotations and labels for injectors (SPEC §4.5.2). The same
// ledger-api contract runs under a different injector in each cluster;
// only the platform's values and files documents change.

// The Vault Agent injector writes database credentials to the file input's
// path. agent-inject and the role apply to the whole pod, so they are
// shared; the per-secret annotations use the input's name as the agent's
// secret name, and put the file at exactly {dir}/{file}, the input's path.
vaultAgentValues: {
	LOG_LEVEL: "warn"
	podAnnotations: {
		"vault.hashicorp.com/agent-inject": "true"
		"vault.hashicorp.com/role":         "ledger"
	}
}
vaultAgentFiles: "db-creds": injected: {
	provider: "vault-agent"
	podAnnotations: {
		"vault.hashicorp.com/agent-inject-secret-{input}":   "database/creds/ledger"
		"vault.hashicorp.com/agent-inject-template-{input}": "{{- with secret \"database/creds/ledger\" -}}{{ .Data | toJSON }}{{- end }}"
		"vault.hashicorp.com/secret-volume-path-{input}":    "{dir}"
		"vault.hashicorp.com/agent-inject-file-{input}":     "{file}"
	}
}

// Bank-Vaults resolves the vault: reference in the env value; its webhook
// reads the Vault address and role from the pod's annotations.
bankVaultsValues: DB_PASSWORD: injected: {
	provider: "bank-vaults"
	ref:      "vault:database/creds/ledger#password"
	podAnnotations: {
		"vault.security.banzaicloud.io/vault-addr": "https://vault.vault.svc:8200"
		"vault.security.banzaicloud.io/vault-role": "ledger"
	}
}

// Not a secret: the OpenTelemetry operator sets the OTLP endpoint (and
// injects the Java agent) when the pod carries its annotation.
otelValues: OTEL_EXPORTER_OTLP_ENDPOINT: injected: {
	provider: "otel-operator"
	podAnnotations: "instrumentation.opentelemetry.io/inject-java": "true"
}

// Some injectors key on a label: Azure Workload Identity's webhook sets
// AZURE_CLIENT_ID and friends on pods labelled azure.workload.identity/use.
workloadIdentityValues: AZURE_CLIENT_ID: injected: {
	provider: "azure-workload-identity"
	podLabels: "azure.workload.identity/use": "true"
}

vaultAgentCheck: contract.#Validate & {contract: ledger, values: vaultAgentValues, files: vaultAgentFiles}
bankVaultsCheck: contract.#Validate & {contract: ledger, values: bankVaultsValues}
otelCheck: contract.#Validate & {contract: ledger, values: otelValues & workloadIdentityValues}

// Two inputs repeating the same key with the same value is fine.
sharedTwiceCheck: contract.#Validate & {
	contract: ledger
	values: DB_PASSWORD: injected: {provider: "vault-agent", podAnnotations: "vault.hashicorp.com/agent-inject": "true"}
	files: vaultAgentFiles & {"db-creds": injected: podAnnotations: "vault.hashicorp.com/agent-inject": "true"}
}

injectorPodOut: {
	let va = contract.#Render & {contract: ledger, values: vaultAgentValues, files: vaultAgentFiles}
	let bv = contract.#Render & {contract: ledger, values: bankVaultsValues}
	let ot = contract.#Render & {contract: ledger, values: otelValues & workloadIdentityValues}
	vaultAgent: {env: va.env, volumes: va.volumes, podAnnotations: va.podAnnotations, podLabels: va.podLabels}
	bankVaults: {env: bv.env, podAnnotations: bv.podAnnotations, podLabels: bv.podLabels}
	otelAndWorkloadIdentity: {env: ot.env, podAnnotations: ot.podAnnotations, podLabels: ot.podLabels}
}
