package kitchen

import "docuconf.dev/contract"

contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		name: "kitchen-api"
		generator: {language: "go", sdk: "docuconf-go", version: "0.0.0"}
	}
	vars: {
		API_TOKEN: {type: "string", description: "Token for the upstream API", required: true, secret: true}
		BACKOFF: {type: "duration", description: "Retry backoff", encoding: "seconds", min: "1ms", max: "1h"}
		CACHE_TTL: {type: "duration", description: "Cache lifetime", encoding: "iso8601"}
		DATABASE_URL: {type: "url", description: "Database connection string", required: true, secret: true, schemes: ["postgres"]}
		DEBUG: {type: "bool", description: "Verbose logging", default: false}
		FEATURES: {type: "json", description: "Feature switches", maxLength: 200}
		GREETING: {type: "string", description: "Greeting shown to users", minLength: 2, maxLength: 12, pattern: "^[^0-9]+$"}
		HOSTS: {type: "list", description: "Peer hosts", items: "string", encoding: "indexed", itemMinLength: 1}
		IDLE: {type: "duration", description: "Idle timeout", encoding: "timespan"}
		LEGACY_MODE: {type: "bool", description: "The old code path", deprecated: message: "use MODE instead"}
		LOG_LEVEL: {type: "enum", description: "Minimum log level", values: ["debug", "info", "warn"], default: "info"}
		MEMORY_LIMIT: {type: "int", description: "Container memory limit in bytes"}
		NODE_NAME: {type: "string", description: "The node the pod runs on"}
		PROFILE: {type: "enum", description: "Deployment profile", values: ["dev", "prod"]}
		PORTS: {type: "list", description: "Ports to listen on", items: "int", encoding: "csv", separator: ";", minItems: 1, maxItems: 3, itemMin: 1, itemMax: 65535}
		RATIO: {type: "float", description: "Sampling ratio", min: 0, max: 1}
		REGION: {type: "string", description: "Cloud region", required: true}
		SIGNING_KEYS: {type: "keySet", description: "Keys that sign tokens", encoding: "indexed"}
		TAGS: {type: "list", description: "Tags for metrics", items: "string", encoding: "json"}
		TIMEOUT: {type: "duration", description: "Request timeout", min: "1s", max: "5m"}
		UPSTREAM: {type: "url", description: "Upstream base URL", schemes: ["http", "https"], maxLength: 100}
		WORKERS: {type: "int", description: "Worker count", min: 1, max: 64}
	}
	profiles: {
		selector: "PROFILE"
		default: "dev"
		defaults: dev: {REGION: "local"}
	}
}
