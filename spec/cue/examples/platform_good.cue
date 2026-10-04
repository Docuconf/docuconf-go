package examples

import "docuconf.dev/contract"

// What a Crossplane composition (function-cue) or CI step does with
// the values a team puts in its claim.
goodValues: {
	DATABASE_URL: secretKeyRef: {name: "billing-db", key: "url"}
	PORT:            9090
	LOG_LEVEL:       "warn"
	REQUEST_TIMEOUT: "45s"
	ALLOWED_ORIGINS: ["https://app.example.com", "https://admin.example.com"]
}

good: contract.#Validate & {contract: billing, values: goodValues}

goodEnv: (contract.#Render & {contract: billing, values: goodValues}).env
