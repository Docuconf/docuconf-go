package examples

import "docuconf.dev/contract"

// want: provider: invalid value
// A provider names who supplies the value, as a lowercase label.
bad: contract.#Validate & {
	contract: billing
	values: {
		DATABASE_URL: injected: {provider: "Bank Vaults", ref: "vault:secret/data/db#url"}
		ALLOWED_ORIGINS: ["https://a.example.com"]
	}
}
