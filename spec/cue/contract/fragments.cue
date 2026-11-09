package contract

import "strings"

// #Fragments are the well-known fragments (SPEC §4.8): variables that a
// runtime, framework or library reads, which the app never reads itself
// and so never declares. An app includes a fragment by name, and its
// exported contract carries the fragment's variables inline, so the
// platform may set them under the strict unknown-variable check.
//
// A fragment is keyed name@major. Adding an optional variable to a
// fragment keeps its major version; changing or removing one, or making
// one required, is a new major version. spec/fragments.json is this, as
// JSON, for SDKs.
#Fragments: {
	[=~"^[a-z][a-z0-9-]*@[1-9][0-9]*$"]: {
		description: strings.MinRunes(5)
		vars: [N=#EnvName]: #Var & {name: N}
	}

	"go-runtime@1": {
		description: "The Go runtime"
		vars: {
			GOMAXPROCS: {
				type:        "int"
				description: "Maximum number of OS threads running Go code at once"
				min:         1
			}
			GOMEMLIMIT: {
				type:        "string"
				description: "Soft memory limit for the Go runtime, in bytes with an optional unit suffix, or off"
				pattern:     "^([0-9]+(B|KiB|MiB|GiB|TiB)?|off)$"
				examples: ["512MiB", "off"]
			}
			GOGC: {
				type:        "string"
				description: "Garbage collection target percentage, or off"
				pattern:     "^(-?[0-9]+|off)$"
				examples: ["100", "off"]
			}
			GODEBUG: {
				type:        "string"
				description: "Comma-separated runtime debugging settings, name=value"
			}
		}
	}

	"node@1": {
		description: "The Node.js runtime"
		vars: {
			NODE_ENV: {
				type:        "string"
				description: "The environment frameworks and libraries tune themselves for, such as production"
				examples: ["production", "development"]
			}
			NODE_OPTIONS: {
				type:        "string"
				description: "Command-line options for the node process, such as --max-old-space-size"
			}
			UV_THREADPOOL_SIZE: {
				type:        "int"
				description: "Size of the libuv thread pool for file system, DNS and crypto work"
				min:         1
				max:         1024
			}
		}
	}

	"otel@1": {
		description: "OpenTelemetry SDK configuration"
		vars: {
			OTEL_SERVICE_NAME: {
				type:        "string"
				description: "Service name reported in telemetry"
			}
			OTEL_RESOURCE_ATTRIBUTES: {
				type:        "string"
				description: "Resource attributes, comma-separated key=value pairs"
			}
			OTEL_EXPORTER_OTLP_ENDPOINT: {
				type:        "url"
				description: "Base URL of the OTLP collector"
				schemes: ["http", "https"]
			}
			OTEL_EXPORTER_OTLP_PROTOCOL: {
				type:        "enum"
				description: "Transport protocol for OTLP export"
				values: ["grpc", "http/protobuf", "http/json"]
			}
			OTEL_EXPORTER_OTLP_HEADERS: {
				type:        "string"
				description: "Headers sent with every OTLP export, comma-separated key=value pairs"
				// Collector credentials travel in these headers.
				secret: true
			}
			OTEL_TRACES_SAMPLER: {
				type:        "enum"
				description: "Sampler for traces"
				values: ["always_on", "always_off", "traceidratio", "parentbased_always_on", "parentbased_always_off", "parentbased_traceidratio", "jaeger_remote", "parentbased_jaeger_remote", "xray"]
			}
			OTEL_TRACES_SAMPLER_ARG: {
				type:        "string"
				description: "Argument for the sampler, such as the ratio for traceidratio"
			}
			OTEL_TRACES_EXPORTER: {
				type:        "string"
				description: "Trace exporters, comma-separated, such as otlp or none"
			}
			OTEL_METRICS_EXPORTER: {
				type:        "string"
				description: "Metric exporters, comma-separated, such as otlp or none"
			}
			OTEL_LOGS_EXPORTER: {
				type:        "string"
				description: "Log exporters, comma-separated, such as otlp or none"
			}
			OTEL_PROPAGATORS: {
				type:        "string"
				description: "Context propagators, comma-separated, such as tracecontext,baggage"
			}
			OTEL_SDK_DISABLED: {
				type:        "bool"
				description: "Disable the OpenTelemetry SDK entirely"
			}
		}
	}

	"aws-sdk@1": {
		description: "The AWS SDKs"
		vars: {
			AWS_REGION: {
				type:        "string"
				description: "AWS Region the SDK sends requests to"
				pattern:     "^[a-z]{2}(-[a-z]+)+-[0-9]+$"
				examples: ["eu-west-1"]
			}
			AWS_DEFAULT_REGION: {
				type:        "string"
				description: "AWS Region the SDK falls back to when AWS_REGION is unset"
				pattern:     "^[a-z]{2}(-[a-z]+)+-[0-9]+$"
			}
			AWS_ROLE_ARN: {
				type:        "string"
				description: "IAM role the SDK assumes with a web identity token"
				pattern:     "^arn:aws[a-z-]*:iam::[0-9]{12}:role/.+$"
			}
			AWS_WEB_IDENTITY_TOKEN_FILE: {
				type:        "string"
				description: "Path of the web identity token the SDK exchanges for credentials"
			}
			AWS_STS_REGIONAL_ENDPOINTS: {
				type:        "enum"
				description: "Whether STS calls go to the regional or the global endpoint"
				values: ["regional", "legacy"]
			}
			AWS_ENDPOINT_URL: {
				type:        "url"
				description: "Endpoint every AWS service client uses instead of its default"
				schemes: ["http", "https"]
			}
		}
	}

	"rails@1": {
		description: "Ruby on Rails and Puma"
		vars: {
			RAILS_ENV: {
				type:        "string"
				description: "The Rails environment, which selects config/environments/{env}.rb"
				examples: ["production"]
			}
			RAILS_MAX_THREADS: {
				type:        "int"
				description: "Threads per Puma worker, and the database pool size"
				min:         1
			}
			WEB_CONCURRENCY: {
				type:        "int"
				description: "Number of Puma worker processes"
				min:         0
			}
			RAILS_LOG_TO_STDOUT: {
				type:        "string"
				description: "Log to standard output when set to any value"
			}
			RAILS_SERVE_STATIC_FILES: {
				type:        "string"
				description: "Serve files in public/ from Rails when set to any value"
			}
			RAILS_MASTER_KEY: {
				type:        "string"
				description: "Key that decrypts config/credentials.yml.enc"
				secret:      true
				pattern:     "^[0-9a-f]{32}$"
			}
		}
	}

	"dotnet-runtime@1": {
		description: ".NET and ASP.NET Core hosting"
		vars: {
			ASPNETCORE_URLS: {
				type:        "string"
				description: "Addresses Kestrel listens on, separated by semicolons"
				examples: ["http://+:8080"]
			}
			ASPNETCORE_HTTP_PORTS: {
				type:        "list"
				description: "HTTP ports Kestrel listens on, on every address"
				items:       "int"
				separator:   ";"
				itemMin:     1
				itemMax:     65535
			}
			ASPNETCORE_HTTPS_PORTS: {
				type:        "list"
				description: "HTTPS ports Kestrel listens on, on every address"
				items:       "int"
				separator:   ";"
				itemMin:     1
				itemMax:     65535
			}
			DOTNET_GCHEAPHARDLIMIT: {
				type:        "string"
				description: "Maximum GC heap size, in bytes, as a hexadecimal number"
				pattern:     "^(0[xX])?[0-9A-Fa-f]+$"
			}
			DOTNET_SYSTEM_GLOBALIZATION_INVARIANT: {
				type:        "enum"
				description: "Run without culture data, as in a container image without ICU"
				values: ["true", "false", "1", "0"]
			}
		}
	}
}

// #FragmentRef names a well-known fragment, name@major.
#FragmentRef: or([for k, _ in #Fragments {k}])
