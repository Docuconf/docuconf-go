package examples

import "docuconf.dev/contract"

// want: _suppliedBy.CATALOG__PAGESIZE
// A variable comes from one place; the environment would silently win.
bad: contract.#Validate & {
	contract: catalog
	values: {CATALOG__DBPASSWORD: secretKeyRef: {name: "db", key: "pw"}, CATALOG__PAGESIZE: 30}
	overlays: platform: {CATALOG__PAGESIZE: 50, CATALOG__SEARCH__URL: "https://search.internal"}
}
