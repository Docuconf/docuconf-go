package examples

import "docuconf.dev/contract"

// want: FEATURE_NEW_CHECKOUT: field not allowed
// Undeclared variable.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
FEATURE_NEW_CHECKOUT: true
}}
