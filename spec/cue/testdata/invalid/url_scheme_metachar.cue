package examples

import "docuconf.dev/contract"

// want: DATABASE_URL.literal
// "+" in a scheme is literal, so "postgresqllasyncpg" must not match "postgresql+asyncpg".
bad: contract.#Validate & {contract: schemesContract, values: DATABASE_URL: "postgresqllasyncpg://db/app"}
