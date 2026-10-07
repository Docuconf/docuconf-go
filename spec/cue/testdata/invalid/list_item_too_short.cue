package examples

import "docuconf.dev/contract"

// want: checks.BRANCHES.literal
// "B" is 1 character, below itemMinLength 2.
bad: contract.#Validate & {contract: lengths, values: {
	BRANCHES: ["BE", "B"]
}}
