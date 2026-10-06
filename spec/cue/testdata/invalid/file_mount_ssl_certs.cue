package examples

import "docuconf.dev/contract"

// want: _mountNotReserved
// Mounting at /etc/ssl/certs would hide the system CA store.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {}
	files: {ca: {type: "caBundle", description: "Private CA bundle", path: "/etc/ssl/certs/private-ca.pem"}}
}
