package examples

import "docuconf.dev/contract"

// want: DATABASE_URL.value: conflicting values
// Secret given as plaintext.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: "postgres://u:p@db/x"
ALLOWED_ORIGINS: ["https://a.example.com"]
}}
