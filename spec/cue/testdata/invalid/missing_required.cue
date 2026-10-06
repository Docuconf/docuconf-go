package examples

import "docuconf.dev/contract"

// want: missingRequired.ALLOWED_ORIGINS
// ALLOWED_ORIGINS is required.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
}}
