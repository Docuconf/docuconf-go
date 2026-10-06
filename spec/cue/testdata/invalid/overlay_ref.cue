package examples

import "docuconf.dev/contract"

// want: literalOnly
// A file holds values, not Kubernetes references.
bad: contract.#Validate & {
	contract: catalog
	values: {CATALOG__DBPASSWORD: secretKeyRef: {name: "db", key: "pw"}}
	overlays: platform: {CATALOG__PAGESIZE: configMapKeyRef: {name: "c", key: "k"}, CATALOG__SEARCH__URL: "https://search.internal"}
}
