package examples

import "docuconf.dev/contract"

// want: missingRequired.INVENTORY__WAREHOUSEAPI
// There is no appsettings.Development.json, so nothing supplies the warehouse URL.
bad: contract.#Validate & {contract: inventory, values: {
	DOTNET_ENVIRONMENT: "Development"
	INVENTORY__DBPASSWORD: secretKeyRef: {name: "db", key: "password"}
}}
