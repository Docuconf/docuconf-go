package examples

import "docuconf.dev/contract"

// want: missingRequired.license
// The licence file is required and has no source.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {}
	files: {routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
	"serving-tls": certificate: {name: "gw", secretName: "gw"}}
	#schemas: #GatewaySchemas
}

// Valid sources for every input, so each case fails for one reason.
gatewayValues: {
	POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
}
#GatewaySchemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
