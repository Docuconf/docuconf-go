package examples

import "docuconf.dev/contract"

// want: out of bound <=65535
// PORT > max.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
PORT: 70000
}}
