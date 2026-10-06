package examples

import "docuconf.dev/contract"

// Platform teams add their own rules by unifying a policy with the
// values. The app contract says what is valid for the app; the policy
// says what an environment allows. Both must hold.
prodPolicy: {
	LOG_LEVEL?:       "info" | "warn" | "error"
	STRIPE_API_BASE?: "https://api.stripe.com"
}

goodProd: contract.#Validate & {contract: billing, values: goodValues & prodPolicy}
