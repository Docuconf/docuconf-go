package examples

import "docuconf.dev/contract"

// want: checks.GOMAXPROCS
// A fragment variable is checked like any other.
bad: contract.#Validate & {contract: worker, values: {QUEUE_URL: "amqps://mq.internal/jobs", GOMAXPROCS: 0}}
