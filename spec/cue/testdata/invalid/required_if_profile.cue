package examples

import "docuconf.dev/contract"

// want: missingRequired.TLS_CLIENT_CA_SUBJECT
// The production profile turns TLS on, so its inputs are required.
bad: contract.#Validate & {
	contract: relay
	values: APP_ENV: "production"
	files: "serving-tls": certificate: {name: "relay", secretName: "relay-tls"}
}
