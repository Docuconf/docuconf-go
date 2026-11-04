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
		API_TOKEN: {type: "string", description: "Token for the upstream API", required: true, secret: true, configKey: "api.token"}
		BACKOFF: {type: "duration", description: "Retry backoff", encoding: "seconds", min: "1ms", max: "1h", configKey: "kitchen.retry.backoff"}
		CACHE_TTL: {type: "duration", description: "Cache lifetime", encoding: "iso8601", configKey: "cache.ttl"}
		DATABASE_URL: {type: "url", description: "Database connection string", required: true, secret: true, schemes: ["postgres"]}
		DEBUG: {type: "bool", description: "Verbose logging", default: false}
		FEATURES: {type: "json", description: "Feature switches", maxLength: 200, configKey: "features"}
		GREETING: {type: "string", description: "Greeting shown to users", minLength: 2, maxLength: 12, pattern: "^[^0-9]+$", configKey: "greeting"}
		HOSTS: {type: "list", description: "Peer hosts", items: "string", encoding: "indexed", itemMinLength: 1}
		IDLE: {type: "duration", description: "Idle timeout", encoding: "timespan"}
		LEGACY_MODE: {type: "bool", description: "The old code path", deprecated: message: "use MODE instead", configKey: "legacy"}
		LOG_LEVEL: {type: "enum", description: "Minimum log level", values: ["debug", "info", "warn"], default: "info", configKey: "logging.level"}
		MEMORY_LIMIT: {type: "int", description: "Container memory limit in bytes"}
		NODE_NAME: {type: "string", description: "The node the pod runs on"}
		PROFILE: {type: "enum", description: "Deployment profile", values: ["dev", "prod"], configKey: "profile"}
		PORTS: {type: "list", description: "Ports to listen on", items: "int", encoding: "csv", separator: ";", minItems: 1, maxItems: 3, itemMin: 1, itemMax: 65535, configKey: "kitchen.ports"}
		RATIO: {type: "float", description: "Sampling ratio", min: 0, max: 1}
		REGION: {type: "string", description: "Cloud region", required: true, configKey: "region"}
		SIGNING_KEYS: {type: "keySet", description: "Keys that sign tokens", encoding: "indexed"}
		TAGS: {type: "list", description: "Tags for metrics", items: "string", encoding: "json"}
		TENANT: {type: "string", description: "Tenant this deployment serves", required: true, configKey: "tenant"}
		TIMEOUT: {type: "duration", description: "Request timeout", min: "1s", max: "5m", configKey: "kitchen.timeout"}
		UPSTREAM: {type: "url", description: "Upstream base URL", schemes: ["http", "https"], maxLength: 100}
		WORKERS: {type: "int", description: "Worker count", min: 1, max: 64, configKey: "kitchen.workers"}
	}
	files: {
		settings: {type: "config", format: "yaml", description: "Kitchen settings", path: "/etc/kitchen/settings/settings.yaml", pathEnv: "SETTINGS_PATH", maxSize: 400}
		rules: {type: "config", format: "json", description: "Routing rules", path: "/etc/kitchen/rules/rules.json", maxSize: 60}
		legacy: {type: "config", format: "toml", description: "Legacy settings", path: "/etc/kitchen/legacy/app.toml"}
		motd: {type: "text", description: "Message of the day", path: "/etc/kitchen/motd/motd.txt", minLength: 3, maxLength: 40, pattern: "^[^<]*$", reload: "watch", deprecated: message: "use GREETING"}
		ca: {type: "caBundle", description: "Trusted CA certificates", path: "/etc/kitchen/ca/ca.pem", minCertificates: 2}
		"serving-tls": {type: "tls", description: "Serving certificate", path: "/etc/kitchen/tls", dnsNames: ["kitchen.example.com"], keyAlgorithms: ["ECDSA"], minRemaining: "720h"}
		keystore: {type: "keystore", format: "pkcs12", description: "Client keystore", path: "/etc/kitchen/ks/client.p12", passwordVar: "API_TOKEN"}
		model: {type: "binary", description: "Ranking model", path: "/models/rank.bin"}
	}
	overlays: {
		appjson: {format: "json", description: "Settings for the .NET host", path: "/etc/kitchen/overlay-json/appsettings.json", keySeparator: "."}
		appyaml: {format: "yaml", path: "/etc/kitchen/overlay-yaml/application.yaml", keySeparator: "."}
		apptoml: {format: "toml", path: "/etc/kitchen/overlay-toml/app.toml", keySeparator: ".", reload: "watch"}
	}
	profiles: {
		selector: "PROFILE"
		default: "dev"
		defaults: dev: {REGION: "local"}
	}
}
