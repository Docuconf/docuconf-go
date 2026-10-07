package examples

import "docuconf.dev/contract"

// want: withinMaxLength
// {"maxRecords":100000,"dryRun":false} is 36 characters, above maxLength 35.
bad: contract.#Validate & {contract: lengths, values: {
	LIMITS: {maxRecords: 100000, dryRun: false}
}}
