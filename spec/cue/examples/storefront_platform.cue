package examples

import "docuconf.dev/contract"

// A platform still setting a deprecated variable: #Validate accepts the
// values and lists it in deprecatedSet, a warning (SPEC §4.2). The key
// set comes from one Secret key, rendered like any secret.
storefrontValues: {
	STOREFRONT__BANNER: "Summer sale"
	STOREFRONT__PAYMENTS__WEBHOOKKEYS: secretKeyRef: {name: "payments-webhook", key: "keys"}
	STOREFRONT__DB__PASSWORD: secretKeyRef: {name: "storefront-db", key: "password"}
}

storefrontGood: contract.#Validate & {contract: storefront, values: storefrontValues, files: {
	"signing-key": secret: {name: "storefront-signing", key: "key.pem"}
}}

storefrontDeprecatedSet: storefrontGood.deprecatedSet

storefrontEnv: (contract.#Render & {contract: storefront, values: storefrontValues}).env
