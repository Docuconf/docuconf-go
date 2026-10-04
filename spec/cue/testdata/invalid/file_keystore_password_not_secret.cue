package examples

import "docuconf.dev/contract"

// want: _passwordIsSecretVar
// A keystore password must be a secret variable.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {KS_PASSWORD: {type: "string", description: "Keystore password"}}
	files: {ks: {type: "keystore", format: "pkcs12", description: "Client keystore", path: "/etc/gw/ks/keystore.p12", passwordVar: "KS_PASSWORD"}}
}
