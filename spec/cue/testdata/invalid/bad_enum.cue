package examples

import "docuconf.dev/contract"

// want: checks.LOG_LEVEL
// LOG_LEVEL not in values.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
LOG_LEVEL: "verbose"
}}
