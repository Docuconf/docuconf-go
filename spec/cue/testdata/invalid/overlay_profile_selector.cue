package examples

import "docuconf.dev/contract"

// want: notProfileSelector
// The profile selector picks which files load, so an overlay cannot set it.
_selectorApp: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "selector-app", generator: {language: "dotnet", sdk: "Docuconf.Options", version: "0.2.0"}}
	vars: DOTNET_ENVIRONMENT: {type: "string", description: "Hosting environment", default: "Production", configKey: "Environment"}
	profiles: {selector: "DOTNET_ENVIRONMENT", default: "Production", defaults: {}}
	overlays: platform: {format: "json", path: "/app/config/appsettings.Production.json", keySeparator: ":"}
}

bad: contract.#Validate & {
	contract: _selectorApp
	overlays: platform: DOTNET_ENVIRONMENT: "Staging"
}
