package examples

import "docuconf.dev/contract"

// want: checks.BRANCHES.literal
// "ZÜRICH" is 6 characters, above itemMaxLength 4.
bad: contract.#Validate & {contract: lengths, values: {
	BRANCHES: ["BE", "ZÜRICH"]
}}
