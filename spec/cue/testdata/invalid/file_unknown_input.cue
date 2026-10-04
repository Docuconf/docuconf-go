package examples

import "docuconf.dev/contract"

// want: files.motd: field not allowed
// A source for a file input the app does not declare.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {}
	files: {routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
	"serving-tls": certificate: {name: "gw", secretName: "gw"}
	license: inline: "ABCDE-12345-FGHIJ-67890"
	motd: inline: "hello"}
	#schemas: #GatewaySchemas
}

// Valid sources for every input, so each case fails for one reason.
gatewayValues: {
	POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
}
#GatewaySchemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
