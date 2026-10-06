package examples

import "docuconf.dev/contract"

// want: invalid value 1000 (out of bound <=500)
// Overlay values are checked like env values.
bad: contract.#Validate & {
	contract: catalog
	values: {CATALOG__DBPASSWORD: secretKeyRef: {name: "db", key: "pw"}}
	overlays: platform: {CATALOG__PAGESIZE: 1000, CATALOG__SEARCH__URL: "https://search.internal"}
}
