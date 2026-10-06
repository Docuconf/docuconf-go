package examples

import "docuconf.dev/contract"

// want: LOG_LEVEL
// The contract allows debug, but prod policy does not.
prodPolicy: LOG_LEVEL?: "info" | "warn" | "error"

bad: contract.#Validate & {contract: billing, values: prodPolicy & {
	DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
	ALLOWED_ORIGINS: ["https://a.example.com"]
	LOG_LEVEL: "debug"
}}
