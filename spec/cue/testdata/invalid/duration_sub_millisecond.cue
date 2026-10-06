package examples

import "docuconf.dev/contract"

// want: wholeMilliseconds
// A timespan-encoded duration cannot carry nanoseconds.
bad: contract.#Validate & {contract: orders, values: {
	ORDERS__BROKERS: ["kafka-0:9092"]
	ORDERS__CHECKOUTTIMEOUT: "1s500ns"
}}
