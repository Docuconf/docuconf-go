package examples

import "docuconf.dev/contract"

// want: notSecret
// A secret never goes in an overlay: overlays are ConfigMaps.
bad: contract.#Validate & {
	contract: catalog
	values: {}
	overlays: platform: {CATALOG__DBPASSWORD: "hunter2", CATALOG__SEARCH__URL: "https://search.internal"}
}
