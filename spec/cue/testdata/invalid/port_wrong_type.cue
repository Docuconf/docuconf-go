package examples

import "docuconf.dev/contract"

// want: mismatched types int and string
// PORT given as string.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
PORT: "8080"
}}
