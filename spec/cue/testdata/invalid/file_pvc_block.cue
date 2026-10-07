package examples

import "docuconf.dev/contract"

// want: filesystemClaim
// A raw block device cannot be mounted as a directory.
bad: contract.#Validate & {
	contract: batch
	values: BATCH_DATE: "20261007"
	files: batchFiles & {orders: pvc: volumeMode: "Block"}
}
