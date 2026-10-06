package examples

import "docuconf.dev/contract"

// want: invalid value 4294967296 (out of bound <=2147483647)
// ORDERS__PARTITIONS items are 32-bit ints.
bad: contract.#Validate & {contract: orders, values: {
ORDERS__BROKERS: ["kafka-0:9092"]
ORDERS__PARTITIONS: [1, 4294967296]
}}
