package examples

import "docuconf.dev/contract"

// want: writableClaim
// A read-write mount of a claim that only allows ReadOnlyMany.
bad: contract.#Validate & {
	contract: batch
	values: BATCH_DATE: "20261007"
	files: batchFiles & {orders: pvc: {readOnly: false, accessModes: ["ReadOnlyMany"]}}
}
