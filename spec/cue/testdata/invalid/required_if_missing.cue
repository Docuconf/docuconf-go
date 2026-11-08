package examples

import "docuconf.dev/contract"

// want: missingRequired.TLS_CLIENT_CA_SUBJECT
// TLS is on, and the platform does not set what it requires.
bad: contract.#Validate & {
	contract: relay
	values: TLS_ENABLED: true
	files: "serving-tls": certificate: {name: "relay", secretName: "relay-tls"}
}
