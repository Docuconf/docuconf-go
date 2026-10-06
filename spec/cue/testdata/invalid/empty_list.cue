package examples

import "docuconf.dev/contract"

// want: MinItems
// ALLOWED_ORIGINS minItems 1.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: []
}}
