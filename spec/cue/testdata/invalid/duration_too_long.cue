package examples

import "docuconf.dev/contract"

// want: REQUEST_TIMEOUT.atMostMax
// REQUEST_TIMEOUT > 5m.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
REQUEST_TIMEOUT: "10m"
}}
