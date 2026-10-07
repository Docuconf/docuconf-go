package examples

import "docuconf.dev/contract"

// want: secretFromSecretStore
// Anything that mounts the claim can read a file on it, so secret inputs
// never come from a PVC.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {}
	files: {routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
		license: inline: "ABCDE-12345-FGHIJ-67890"
		"serving-tls": pvc: claimName: "certs"
	}
	#schemas: #GatewaySchemas
}

// Valid sources for every other input, so the case fails for one reason.
gatewayValues: {
	POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
}
#GatewaySchemas: {routes: #RoutesSchema, RATE_LIMITS: #RateLimitsSchema}
