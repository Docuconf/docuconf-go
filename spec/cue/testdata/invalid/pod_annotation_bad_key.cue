package examples

import "docuconf.dev/contract"

// want: podAnnotationKey
// Annotation keys are qualified names: no spaces, a name of at most 63
// characters after the optional prefix.
bad: contract.#Validate & {
	contract: ledger
	values: OTEL_EXPORTER_OTLP_ENDPOINT: injected: {
		provider: "otel-operator"
		podAnnotations: "instrumentation.opentelemetry.io/inject java": "true"
	}
}
