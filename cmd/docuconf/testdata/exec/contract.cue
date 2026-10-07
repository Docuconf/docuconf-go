// A batch job's contract, for the check and exec tests.
package batch

import "docuconf.dev/contract"

contract.#Contract & {
	metadata: {name: "orders-batch", generator: {language: "cobol", sdk: "docuconf-test", version: "1"}}
	vars: {
		PORT: {type: "int", description: "Metrics port", min: 1, max: 65535, default: 8080}
		DATABASE_URL: {type: "url", description: "Orders database", required: true, secret: true, schemes: ["postgres"]}
		ALLOWED_ORIGINS: {type: "list", description: "Origins allowed to call back", items: "string", minItems: 1, default: ["http://localhost:3000"]}
		REQUEST_TIMEOUT: {type: "duration", description: "Time allowed per request", min: "1s", max: "5m", default: "30s"}
	}
	files: {
		orders: {type: "text", description: "Orders to process", path: "/data/orders.txt", pathEnv: "ORDERS_FILE", required: true, maxSize: 1024}
	}
}
