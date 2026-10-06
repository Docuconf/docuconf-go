package examples

import "docuconf.dev/contract"

// URL schemes may contain regex metacharacters: postgresql+asyncpg must
// match itself, and a "." in a scheme must not match any character.
schemesContract: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "schemes", generator: {language: "python", sdk: "docuconf-pydantic", version: "0.1.0"}}
	vars: DATABASE_URL: {
		type:        "url"
		description: "Async Postgres connection string"
		schemes: ["postgresql+asyncpg"]
	}
}

schemesOK: contract.#Validate & {contract: schemesContract, values: DATABASE_URL: "postgresql+asyncpg://db/app"}
