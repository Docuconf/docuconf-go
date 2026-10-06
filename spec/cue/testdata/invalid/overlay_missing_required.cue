package examples

import "docuconf.dev/contract"

// want: missingRequired.CATALOG__SEARCH__URL
// A required variable must come from the environment, a profile or an overlay.
bad: contract.#Validate & {
	contract: catalog
	values: {CATALOG__DBPASSWORD: secretKeyRef: {name: "db", key: "pw"}}
	overlays: platform: {CATALOG__PAGESIZE: 50}
}
