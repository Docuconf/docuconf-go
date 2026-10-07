package examples

import "docuconf.dev/contract"

// want: conflictingPodLabel."azure.workload.identity/use"."(shared) and AZURE_CLIENT_ID"
// An input's label disagrees with the values document's shared one.
bad: contract.#Validate & {
	contract: ledger
	values: {
		podLabels: "azure.workload.identity/use": "false"
		AZURE_CLIENT_ID: injected: {provider: "azure-workload-identity", podLabels: "azure.workload.identity/use": "true"}
	}
}
