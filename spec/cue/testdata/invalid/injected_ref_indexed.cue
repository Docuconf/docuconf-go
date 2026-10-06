package examples

import "docuconf.dev/contract"

// want: injectedRefNotIndexed
// An indexed list is spread over NAME__0, NAME__1…; one injected env value
// cannot carry it.
bad: contract.#Validate & {
	contract: orders
	values: ORDERS__BROKERS: injected: {provider: "bank-vaults", ref: "vault:secret/data/kafka#brokers"}
}
