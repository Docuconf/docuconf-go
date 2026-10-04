package examples

import "docuconf.dev/contract"

// want: REQUEST_TIMEOUT.value
// REQUEST_TIMEOUT not a duration.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
REQUEST_TIMEOUT: "30 seconds"
}}
