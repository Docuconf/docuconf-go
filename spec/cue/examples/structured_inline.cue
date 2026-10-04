package examples

import "docuconf.dev/contract"

// A config file's inline content given as data rather than text, as a
// Helm values file would hold it. It is checked against the file's
// schema and serialised as YAML, the file's format.
structuredRoutes: {
	routes: inline: routes: [{match: "/billing", upstream: "http://billing-api.billing.svc:8080", timeout: "5s"}]
	"serving-tls": certificate: {name: "gw", secretName: "gw"}
	license: inline: "ABCDE-12345-FGHIJ-67890\n"
}

structuredCheck: contract.#Validate & {
	contract: gateway
	values: {
		POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
		PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
	}
	files:    structuredRoutes
	#schemas: #GatewaySchemas
}

structuredOut: (contract.#Render & {contract: gateway, files: structuredRoutes}).configMaps[0]
