package examples

import "docuconf.dev/contract"

// want: wellFormed
// The routes file is not valid YAML.
bad: contract.#Validate & {
	contract: gateway
	values: gatewayValues & {}
	files: {routes: inline: "routes: [unclosed"
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
