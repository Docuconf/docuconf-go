package examples

import "docuconf.dev/contract"

// want: structuredOnlyForConfig
// Only config files can take structured content; a licence file is text.
bad: contract.#Validate & {
	contract: gateway
	values: {
		POD_NAMESPACE: fieldRef: fieldPath: "metadata.namespace"
		PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "p", key: "password"}
	}
	files: {
		routes: inline: "routes:\n  - match: /a\n    upstream: http://a.svc\n"
		"serving-tls": certificate: {name: "gw", secretName: "gw"}
		license: inline: key: "ABCDE-12345-FGHIJ-67890"
	}
}
