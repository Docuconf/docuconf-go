package examples

import "docuconf.dev/contract"

// want: tlsSecretType
// An Opaque Secret is not a TLS key pair.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {}
	files: {routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
	license: inline: "ABCDE-12345-FGHIJ-67890"
	"serving-tls": secret: {name: "gw", type: "Opaque"}}
	#schemas: #GatewaySchemas
}

// Valid sources for every input, so each case fails for one reason.
gatewayValues: {
	POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
}
#GatewaySchemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
