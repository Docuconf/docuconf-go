package examples

import "docuconf.dev/contract"

// Values supplied at runtime by injectors rather than by the pod spec.
// Bank-Vaults resolves a reference written into the env value; an
// operator sets a variable itself, so nothing is rendered for it.
injectedValues: {
	DATABASE_URL: injected: {provider: "bank-vaults", ref: "vault:secret/data/billing/db#url"}
	ALLOWED_ORIGINS: injected: provider: "origins-operator"
	LOG_LEVEL: "warn"
}

injectedCheck: contract.#Validate & {contract: billing, values: injectedValues}

injectedEnv: (contract.#Render & {contract: billing, values: injectedValues}).env

// A secret file written by the Vault Agent injector: no volume, no mount;
// the agent writes it at the input's path and the SDK checks it at boot.
injectedFiles: {
	routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
	"serving-tls": certificate: {name: "gw", secretName: "gw"}
	"partner-keystore": injected: provider: "vault-agent"
	license: inline: "ABCDE-12345-FGHIJ-67890\n"
}

injectedFileCheck: contract.#Validate & {
	contract: gateway
	values: {
		POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
		PARTNER_KEYSTORE_PASSWORD: injected: {provider: "vault-agent"}
	}
	files:    injectedFiles
	#schemas: #GatewaySchemas
}

injectedFileOut: {
	let r = contract.#Render & {contract: gateway, files: injectedFiles}
	volumes: [for v in r.volumes {v.name}]
	volumeMounts: [for m in r.volumeMounts {m.mountPath}]
}
