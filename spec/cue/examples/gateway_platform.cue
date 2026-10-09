package examples

import "docuconf.dev/contract"

// What the platform supplies for the gateway in one environment.
gatewayValues: {
	LOG_LEVEL: "warn"
	POD_NAMESPACE: fieldRef: fieldPath:     "metadata.namespace"
	GOMEMLIMIT: resourceFieldRef: resource: "limits.memory"
	RATE_LIMITS: {perMinute: 600, burst: 50}
	PARTNER_KEYSTORE_PASSWORD: secretKeyRef: {name: "partner-keystore", key: "password"}
	// One Secret key holding the keys, "old,new" during a rotation.
	PARTNER_API_KEYS: secretKeyRef: {name: "partner-api-keys", key: "keys"}
}

gatewayFiles: {
	routes: inline: """
		routes:
		  - match: /billing
		    upstream: http://billing-api.billing.svc:8080
		    timeout: 5s
		  - match: /orders
		    upstream: http://orders-api.orders.svc:8080

		"""
	// cert-manager Certificate; the fields after secretName were resolved
	// from its spec by the platform tooling.
	"serving-tls": certificate: {
		name:       "gateway-tls"
		secretName: "gateway-tls"
		dnsNames: ["gateway.internal", "*.example.com"]
		privateKey: algorithm: "ECDSA"
		duration:    "2160h"
		renewBefore: "720h"
	}
	// A trust-manager Bundle writes CA bundles into ConfigMaps.
	"upstream-ca": configMap: {name: "internal-ca-bundle", key: "ca.crt"}
	"partner-keystore": csi: secretProviderClass: "partner-keystore"
	license: inline: "ABCDE-12345-FGHIJ-67890\n"
	geoip: image: reference: "registry.example.com/data/geoip@sha256:0f2b5c8d3e4a1b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c"
}

// The toolchain compiles each contract schema to CUE and passes it in;
// test.sh does the same with `cue import jsonschema:`.
#GatewaySchemas: {
	routes:      #RoutesSchema
	RATE_LIMITS: #RateLimitsSchema
}

gatewayCheck: contract.#Validate & {
	contract: gateway
	values:   gatewayValues
	files:    gatewayFiles
	#schemas: #GatewaySchemas
}

gatewayRendered: contract.#Render & {
	contract: gateway
	values:   gatewayValues
	files:    gatewayFiles
}

// The rendered pieces, without the inputs echoed back.
gatewayOut: {
	env:             gatewayRendered.env
	volumes:         gatewayRendered.volumes
	volumeMounts:    gatewayRendered.volumeMounts
	configMaps:      gatewayRendered.configMaps
	restartTriggers: gatewayRendered.restartTriggers
	podAnnotations:  gatewayRendered.podAnnotations
	podLabels:       gatewayRendered.podLabels
}

// values.schema.json for a Helm chart that deploys the gateway.
gatewayHelmSchema: (contract.#HelmValuesSchema & {contract: gateway}).out
