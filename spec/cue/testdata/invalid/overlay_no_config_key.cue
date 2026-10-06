package examples

import "docuconf.dev/contract"

// want: hasConfigKey
// The overlay needs the configKey to know where the value goes.
bad: contract.#Validate & {
	contract: catalog
	values: {CATALOG__DBPASSWORD: secretKeyRef: {name: "db", key: "pw"}}
	overlays: platform: {CATALOG__TRACEHEADER: "x-trace", CATALOG__SEARCH__URL: "https://search.internal"}
}
