// A COBOL batch job, run as a CronJob, that reads its input files from a
// PersistentVolumeClaim another job writes.
package examples

import "docuconf.dev/contract"

batch: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		name: "orders-batch"
		generator: {language: "cobol", sdk: "docuconf-cobol", version: "0.1.0"}
	}
	vars: BATCH_DATE: {
		type:        "string"
		description: "Business date the run processes, as YYYYMMDD"
		required:    true
		pattern:     "^[0-9]{8}$"
	}
	files: {
		orders: {
			type:        "text"
			description: "Fixed-width order records written by the extract job"
			required:    true
			path:        "/data/orders/ORDERS.DAT"
			pathEnv:     "DD_ORDERS"
			maxSize:     1073741824
		}
		rates: {
			type:        "text"
			description: "Currency rates for the business date"
			required:    true
			path:        "/data/rates/RATES.DAT"
			pathEnv:     "DD_RATES"
		}
	}
}

// Both inputs come from one claim, in different directories of it.
batchFiles: {
	orders: pvc: {claimName: "batch-io", subPath: "incoming/orders"}
	rates: pvc: {claimName: "batch-io", subPath: "reference"}
}

batchCheck: contract.#Validate & {
	contract: batch
	values: BATCH_DATE: "20261007"
	files: batchFiles
}

// One volume for the claim; one read-only mount per input.
batchOut: {
	let r = contract.#Render & {contract: batch, values: BATCH_DATE: "20261007", files: batchFiles}
	env:             r.env
	volumes:         r.volumes
	volumeMounts:    r.volumeMounts
	restartTriggers: r.restartTriggers
}
