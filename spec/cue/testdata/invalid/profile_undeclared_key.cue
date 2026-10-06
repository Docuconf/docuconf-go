package examples

import "docuconf.dev/contract"

// want: APP__TYPO
// A profile can only set declared variables.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "dotnet", sdk: "Docuconf.Options", version: "0.1.0"}}
	vars: {
		DOTNET_ENVIRONMENT: {type: "string", description: "Hosting environment", default: "Production"}
		APP__PORT: {type: "int", description: "HTTP listen port", max: 65535}
		APP__DBPASSWORD: {type: "string", description: "Database password", required: true, secret: true}
	}
	profiles: {selector: "DOTNET_ENVIRONMENT", default: "Production", defaults: Production: APP__TYPO: 1}
}
