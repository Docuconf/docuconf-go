package examples

import "docuconf.dev/contract"

// want: ref: invalid value
// A reference, when given, cannot be empty.
bad: contract.#Validate & {
	contract: billing
	values: {
		DATABASE_URL: injected: {provider: "bank-vaults", ref: ""}
		ALLOWED_ORIGINS: ["https://a.example.com"]
	}
}
