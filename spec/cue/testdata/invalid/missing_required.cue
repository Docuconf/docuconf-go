package examples

import "docuconf.dev/contract"

// want: ALLOWED_ORIGINS: field is required
// ALLOWED_ORIGINS is required.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
}}
