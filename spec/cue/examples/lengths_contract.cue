// A COBOL batch program whose copybook holds fixed-width fields: every
// value must fit its PIC X(n), so URLs, JSON values and list items carry
// length limits.
package examples

import "docuconf.dev/contract"

lengths: contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		name: "ledger-batch"
		generator: {language: "cobol", sdk: "docuconf-cobol", version: "0.1.0"}
	}
	vars: {
		CALLBACK_URL: {
			type:        "url"
			description: "Where to report the run, PIC X(40)"
			schemes: ["https"]
			maxLength: 40
		}
		LIMITS: {
			type:        "json"
			description: "Run limits as JSON, PIC X(35)"
			maxLength:   35
		}
		BRANCHES: {
			type:          "list"
			description:   "Branch codes, OCCURS 1 TO 8 of PIC X(4)"
			items:         "string"
			minItems:      1
			maxItems:      8
			itemMinLength: 2
			itemMaxLength: 4
		}
	}
}

// Every value exactly at its limit; lengths count characters, so "Zürich"
// would not fit a PIC X(4) item, but "Zü01" does.
lengthsValues: {
	CALLBACK_URL: "https://ledger.example.com/runs/callback"
	LIMITS: {maxRecords: 10000, dryRun: false}
	BRANCHES: ["ZÜ01", "BE", "GE02"]
}

lengthsCheck: contract.#Validate & {contract: lengths, values: lengthsValues}
