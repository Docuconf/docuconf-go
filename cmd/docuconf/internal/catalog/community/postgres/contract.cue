// The official postgres image, 13 to 18: the variables its entrypoint
// reads when it initializes the data directory.
package postgres

import "docuconf.dev/contract"

contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		name: "postgres"
		generator: {language: "manual", sdk: "docuconf-catalog", version: "1"}
	}
	vars: {
		POSTGRES_PASSWORD: {
			type:        "string"
			description: "Password of the superuser the entrypoint creates"
			details:     "The entrypoint refuses to start without it unless `POSTGRES_HOST_AUTH_METHOD` is `trust`, which lets anyone who can reach the server connect without a password. Declare that case in your own catalog."
			required:    true
			secret:      true
			minLength:   1
		}
		POSTGRES_USER: {
			type:        "string"
			description: "Name of the superuser the entrypoint creates"
			default:     "postgres"
			minLength:   1
		}
		POSTGRES_DB: {
			type:        "string"
			description: "Name of the database the entrypoint creates; POSTGRES_USER when unset"
			minLength:   1
		}
		POSTGRES_INITDB_ARGS: {
			type:        "string"
			description: "Extra arguments for initdb, such as --data-checksums"
		}
		POSTGRES_INITDB_WALDIR: {
			type:        "string"
			description: "Directory for the write-ahead log, outside the data directory"
			pattern:     "^/"
		}
		POSTGRES_HOST_AUTH_METHOD: {
			type:        "enum"
			description: "Authentication method pg_hba.conf allows for host connections"
			values: ["scram-sha-256", "md5", "password", "reject"]
		}
		PGDATA: {
			type:        "string"
			description: "Data directory, on the volume the server keeps its files on"
			default:     "/var/lib/postgresql/data"
			pattern:     "^/"
		}
	}
}
