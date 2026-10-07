package examples

import "docuconf.dev/contract"

// want: subPath
// subPath stays inside the claim.
bad: contract.#Validate & {
	contract: batch
	values: BATCH_DATE: "20261007"
	files: {
		orders: pvc: {claimName: "batch-io", subPath: "../other"}
		rates: pvc: claimName: "batch-io"
	}
}
