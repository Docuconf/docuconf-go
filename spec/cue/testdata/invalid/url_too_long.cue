package examples

import "docuconf.dev/contract"

// want: checks.CALLBACK_URL.literal
// 41 characters, above maxLength 40.
bad: contract.#Validate & {contract: lengths, values: {
	CALLBACK_URL: "https://ledger.example.com/runs/callbacks"
}}
