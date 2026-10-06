package examples

import "docuconf.dev/contract"

// want: matchesSchema
// Structured content is checked against the file's schema: this route has no upstream.
bad: contract.#Validate & {
	contract: gateway
	values: {
		POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
		PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "p", key: "password"}
	}
	files: {
		routes: inline: routes: [{match: "/billing"}]
		"serving-tls": certificate: {name: "gw", secretName: "gw"}
		license: inline: "ABCDE-12345-FGHIJ-67890"
	}
	#schemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
}
