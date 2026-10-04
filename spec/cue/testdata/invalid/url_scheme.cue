package examples

import "docuconf.dev/contract"

// want: STRIPE_API_BASE.value
// STRIPE_API_BASE must be https.
bad: contract.#Validate & {contract: billing, values: {
DATABASE_URL: secretKeyRef: {name: "db", key: "url"}
ALLOWED_ORIGINS: ["https://a.example.com"]
STRIPE_API_BASE: "http://api.stripe.com"
}}
