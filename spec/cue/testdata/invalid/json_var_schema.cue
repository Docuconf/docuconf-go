package examples

import "docuconf.dev/contract"

// want: checks.RATE_LIMITS.literal
// perMinute must be at least 1.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {RATE_LIMITS: {perMinute: 0}}
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
