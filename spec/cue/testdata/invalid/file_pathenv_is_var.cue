package examples

import "docuconf.dev/contract"

// want: _pathEnvNotInVars
// The renderer sets ROUTES_FILE itself, so the app cannot also declare it.
bad: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "x", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {ROUTES_FILE: {type: "string", description: "Path to the routes file"}}
	files: {routes: {type: "config", format: "yaml", description: "Routing table", path: "/etc/gw/routes.yaml", pathEnv: "ROUTES_FILE"}}
}
