package examples

import "docuconf.dev/contract"

// want: missingRequired.LOG_SCHEMA_URL
// An enum condition, set by the platform.
bad: contract.#Validate & {contract: relay, values: LOG_FORMAT: "json"}
