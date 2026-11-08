package examples

import "docuconf.dev/contract"

// want: missingRequired."serving-tls"
// TLS is on, and the certificate has no source.
bad: contract.#Validate & {
	contract: relay
	values: {TLS_ENABLED: true, TLS_CLIENT_CA_SUBJECT: "CN=Example Client CA"}
}
