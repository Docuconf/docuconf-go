@extern(embed)

// The platform side in plain CUE: what a Crossplane composition function
// (function-cue) or a CI job evaluates for orders-api. The app's contract,
// the environment's values and the platform's policy unify; if they do not,
// there is no Deployment to apply.
package platform

import (
	"docuconf.dev/contract"
	orders "docuconf.dev/orders"
	policy "docuconf.dev/platform:policy"
)

_values: _ @embed(file=values.yaml)
_files:  _ @embed(file=files.yaml)

// The platform's own rules apply on top of the app's contract.
_values: policy

// `cue vet -c` fails here when an input breaks the contract or the policy.
check: contract.#Validate & {
	"contract": orders
	values:     _values
	files:      _files
	#schemas: "tax-rates": #TaxRates
}

_render: contract.#Render & {
	"contract": orders
	values:     _values
	files:      _files
}

// `cue export -e objects`: the ConfigMaps for inline content, then the
// Deployment.
objects: [
	for cm in _render.configMaps {cm},
	{
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: name: orders.metadata.name
		spec: {
			selector: matchLabels: "app.kubernetes.io/name": orders.metadata.name
			template: {
				// What injected inputs ask for (SPEC §4.5.2); none here.
				metadata: {
					labels: {"app.kubernetes.io/name": orders.metadata.name, _render.podLabels}
					if len(_render.podAnnotations) > 0 {annotations: _render.podAnnotations}
				}
				spec: {
					containers: [{
						name:         orders.metadata.name
						image:        "registry.example.com/orders-api:\(orders.metadata.appVersion)"
						env:          _render.env
						volumeMounts: _render.volumeMounts
					}]
					volumes: _render.volumes
				}
			}
		}
	},
]
