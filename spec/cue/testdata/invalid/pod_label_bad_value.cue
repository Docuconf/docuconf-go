package examples

import "docuconf.dev/contract"

// want: podLabelValue
// A label value is at most 63 characters, without slashes.
bad: contract.#Validate & {
	contract: ledger
	values: AZURE_CLIENT_ID: injected: {
		provider: "azure-workload-identity"
		podLabels: "example.com/client": "{input}/primary"
	}
}
