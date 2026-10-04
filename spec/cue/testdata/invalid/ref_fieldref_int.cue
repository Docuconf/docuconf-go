package examples

import "docuconf.dev/contract"

// want: fieldRefIsString
// The Downward API yields strings, but GOMEMLIMIT is an int.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {GOMEMLIMIT: fieldRef: fieldPath: "metadata.name"}
	files: {routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
	"serving-tls": certificate: {name: "gw", secretName: "gw"}
	license: inline: "ABCDE-12345-FGHIJ-67890"}
	#schemas: #GatewaySchemas
}

// Valid sources for every input, so each case fails for one reason.
gatewayValues: {
	POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
}
#GatewaySchemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
