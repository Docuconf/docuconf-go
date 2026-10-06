package examples

import "docuconf.dev/contract"

// want: field not allowed
// An overlay may only set declared variables.
bad: contract.#Validate & {
	contract: catalog
	values: {CATALOG__DBPASSWORD: secretKeyRef: {name: "db", key: "pw"}}
	overlays: platform: {CATALOG__PAGESZE: 50, CATALOG__SEARCH__URL: "https://search.internal"}
}
