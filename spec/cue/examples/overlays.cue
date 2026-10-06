package examples

import "docuconf.dev/contract"

// The platform supplies most settings through the appsettings overlay, in
// native types, and the secret through Bank-Vaults.
catalogValues: CATALOG__DBPASSWORD: injected: {provider: "bank-vaults", ref: "vault:secret/data/catalog/db#password"}

catalogOverlays: platform: {
	CATALOG__CACHETTL: "90s"
	CATALOG__FEATUREDCATEGORIES: ["books", "games"]
	CATALOG__PAGESIZE:          50
	CATALOG__SEARCH__URL:       "https://search.internal"
	LOGGING__LOGLEVEL__DEFAULT: "Warning"
}

catalogCheck: contract.#Validate & {contract: catalog, values: catalogValues, overlays: catalogOverlays}

catalogOut: {
	let r = contract.#Render & {contract: catalog, values: catalogValues, overlays: catalogOverlays}
	env:             r.env
	volumes:         r.volumes
	volumeMounts:    r.volumeMounts
	configMaps:      r.configMaps
	restartTriggers: r.restartTriggers
}

// With reload: restart the ConfigMap is content-hashed and immutable, so a
// change rolls the pods instead of being reloaded in place.
catalogRestartOverlay: {
	let r = contract.#RenderOverlay & {
		service: "catalog-api"
		overlay: {name: "platform", format: "yaml", path: "/app/config/appsettings.Production.yaml", keySeparator: ":", reload: "restart"}
		vars:   catalog.vars
		values: catalogOverlays.platform
	}
	configMap: r.configMap
}
