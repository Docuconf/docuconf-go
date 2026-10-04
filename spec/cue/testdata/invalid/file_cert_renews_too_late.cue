package examples

import "docuconf.dev/contract"

// want: renewsBeforeMinRemaining
// cert-manager would let the certificate get down to 10 days; the app needs 30.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {}
	files: {routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
	license: inline: "ABCDE-12345-FGHIJ-67890"
	"serving-tls": certificate: {name: "gw", secretName: "gw", renewBefore: "240h"}}
	#schemas: #GatewaySchemas
}

// Valid sources for every input, so each case fails for one reason.
gatewayValues: {
	POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
}
#GatewaySchemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
