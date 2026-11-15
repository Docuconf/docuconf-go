// The official nginx image, 1.19 and later: the variables its
// entrypoint scripts read. Variables a template under
// NGINX_ENVSUBST_TEMPLATE_DIR substitutes are the platform's own: declare
// them in your organization's catalog.
package nginx

import "docuconf.dev/contract"

contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		name: "nginx"
		generator: {language: "manual", sdk: "docuconf-catalog", version: "1"}
	}
	vars: {
		NGINX_ENVSUBST_TEMPLATE_DIR: {
			type:        "string"
			description: "Directory of templates the entrypoint fills from the environment"
			default:     "/etc/nginx/templates"
			pattern:     "^/"
		}
		NGINX_ENVSUBST_TEMPLATE_SUFFIX: {
			type:        "string"
			description: "Suffix of the template files"
			default:     ".template"
		}
		NGINX_ENVSUBST_OUTPUT_DIR: {
			type:        "string"
			description: "Directory the filled templates are written to"
			default:     "/etc/nginx/conf.d"
			pattern:     "^/"
		}
		NGINX_ENVSUBST_FILTER: {
			type:        "string"
			description: "Regular expression for the variables a template may substitute"
		}
		NGINX_ENTRYPOINT_QUIET_LOGS: {
			type:        "string"
			description: "Silence the entrypoint's own log lines when set to any value"
		}
		NGINX_ENTRYPOINT_WORKER_PROCESSES_AUTOTUNE: {
			type:        "string"
			description: "Set worker_processes from the container's CPU limit when set to any value"
		}
		NGINX_ENTRYPOINT_LOCAL_RESOLVERS: {
			type:        "string"
			description: "Export the container's DNS resolvers as NGINX_LOCAL_RESOLVERS when set to any value"
		}
	}
}
