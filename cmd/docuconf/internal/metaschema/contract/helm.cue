package contract

import (
	"list"
	"regexp"
	"strings"
)

// #HelmValuesSchema turns a contract into a Helm values.schema.json
// (JSON Schema draft-07, which Helm 3 and 4 both read). Helm then checks
// a chart's `docuconf` values on every lint, template, install and
// upgrade: types, ranges, patterns, enum values, required inputs,
// unknown names, secrets given only as references, and structured
// config-file content against the file's own schema.
//
// What JSON Schema cannot express (duration bounds, certificate details
// resolved from the cluster, platform policy) stays with `docuconf vet`.
#HelmValuesSchema: {
	contract: #Contract

	// A variable is required in the chart unless the default profile
	// supplies it (Helm cannot know which profile a release selects).
	let _profile = [
		if contract.profiles != _|_ if contract.profiles.defaults[contract.profiles.default] != _|_ {
			contract.profiles.defaults[contract.profiles.default]
		},
		{},
	][0]
	// A variable an overlay may carry (non-secret, with a configKey) is
	// satisfied by values or by any overlay; the others only by values.
	let _overlayNames = [if contract.overlays != _|_ for o, _ in contract.overlays {o}]
	let _overlayable = {
		for n, v in contract.vars if !v.secret && v.configKey != _|_ && len(_overlayNames) > 0 {(n): true}
	}
	let _requiredAll = list.SortStrings([for n, v in contract.vars if v.required && _profile[n] == _|_ {n}])
	let _requiredVars = [for n in _requiredAll if _overlayable[n] == _|_ {n}]
	let _requiredEither = [for n in _requiredAll if _overlayable[n] != _|_ {n}]
	let _requiredFiles = list.SortStrings([
		if contract.files != _|_ for n, f in contract.files if f.required {n},
	])

	out: {
		"$schema": "http://json-schema.org/draft-07/schema#"
		type:      "object"
		properties: docuconf: {
			type:                 "object"
			description:          "Inputs for \(contract.metadata.name), checked against its docuconf contract."
			additionalProperties: false
			properties: {
				// Helm adds `global` to the values of a dependency named like this
				// key, which the docuconf library chart is.
				global: description: "Set by Helm for the docuconf library chart; ignored."
				values: {
					type:                 "object"
					description:          "Environment variables, as typed values."
					additionalProperties: false
					properties: {
						for n, v in contract.vars {(n): (#HelmVar & {var: v}).out}
						// Shared by every injector (SPEC §4.5.2).
						podAnnotations: _helmPodAnnotations & {description: "Pod annotations shared by every injector."}
						podLabels: _helmPodLabels & {description: "Pod labels shared by every injector."}
					}
					if len(_requiredVars) > 0 {required: _requiredVars}
				}
				files: {
					type:                 "object"
					description:          "Where each file input comes from."
					additionalProperties: false
					properties: {
						if contract.files != _|_ for n, f in contract.files {(n): (#HelmFile & {file: f}).out}
					}
					if len(_requiredFiles) > 0 {required: _requiredFiles}
				}
				if len(_overlayNames) > 0 {
					overlays: {
						type:                 "object"
						description:          "Values written into config-file overlays instead of the environment, by overlay name."
						additionalProperties: false
						properties: {
							for o, ov in contract.overlays {
								(o): {
									type: "object"
									[if ov.description != _|_ {description: ov.description}, {description: "Values for \(ov.path)."}][0]
									additionalProperties: false
									properties: {
										for n, v in contract.vars if _overlayable[n] != _|_ {
											(n): {
												description: v.description
												anyOf: [(#HelmVar & {var: v}).literal, if !v.required {_helmNull}]
											}
										}
									}
								}
							}
						}
					}
				}
			}
			if len(_requiredEither) > 0 {
				allOf: [for n in _requiredEither {
					anyOf: [
						{required: ["values"], properties: values: required: [n]},
						for o in _overlayNames {required: ["overlays"], properties: overlays: {required: [o], properties: (o): required: [n]}},
					]
				}]
			}
			let req = list.Concat([[if len(_requiredVars) > 0 {"values"}], [if len(_requiredFiles) > 0 {"files"}]])
			if len(req) > 0 {required: req}
		}
		if len(_requiredAll) > 0 || len(_requiredFiles) > 0 {required: ["docuconf"]}
	}
}

#HelmVar: {
	var: #Var
	out: {...}

	let _literal = {
		if var.type == "string" {
			type: "string"
			if var.minLength != _|_ {minLength: var.minLength}
			if var.maxLength != _|_ {maxLength: var.maxLength}
			if var.pattern != _|_ {pattern: var.pattern}
		}
		if var.type == "int" {
			type: "integer"
			if var.min != _|_ {minimum: var.min}
			if var.max != _|_ {maximum: var.max}
		}
		if var.type == "float" {
			type: "number"
			if var.min != _|_ {minimum: var.min}
			if var.max != _|_ {maximum: var.max}
		}
		if var.type == "bool" {type: "boolean"}
		if var.type == "duration" {
			type:    "string"
			pattern: "^([0-9]+(ns|us|ms|s|m|h))+$"
		}
		if var.type == "url" {
			type: "string"
			if var.schemes == _|_ {pattern: "^[a-zA-Z][a-zA-Z0-9+.-]*://[^\\s]+$"}
			if var.schemes != _|_ {
				pattern: "^(\(strings.Join([for x in var.schemes {regexp.QuoteMeta(x)}], "|")))://[^\\s]+$"
			}
		}
		if var.type == "enum" {enum: var.values}
		if var.type == "list" {
			type: "array"
			items: type: [if var.items == "int" {"integer"}, "string"][0]
			if var.items == "int" {
				if var.itemMin != _|_ {items: minimum: var.itemMin}
				if var.itemMax != _|_ {items: maximum: var.itemMax}
			}
			if var.minItems != _|_ {minItems: var.minItems}
			if var.maxItems != _|_ {maxItems: var.maxItems}
		}
		if var.type == "json" && var.schema != _|_ {var.schema}
	}

	// An indexed list spans several variables, so an injector may set it
	// but not resolve one reference into it.
	let injected = [if var.type == "list" if var.encoding == "indexed" {_helmInjectedNoRef}, _helmInjected][0]

	// The schema of a literal value, also used for overlays.
	literal: _literal

	if var.secret {
		out: {
			description: "\(var.description) (secret: supply a secretKeyRef or injected)"
			oneOf: [_helmSecretKeyRef, injected, if !var.required {_helmNull}]
		}
	}
	if !var.secret {
		out: {
			description: var.description
			anyOf: [
				literal,
				if var.type != "list" {_helmConfigMapKeyRef},
				if var.type == "string" {_helmFieldRef},
				if var.type == "int" {_helmResourceFieldRef},
				injected,
				if !var.required {_helmNull},
			]
		}
	}
}

// Ordinary (not definition) values, so callers can add a description.
_helmRef: {
	key: string
	fields: [...string]
	out: {
		type: "object"
		required: [key]
		// Another source's key may remain as null: overlays switch sources
		// with `old: null`, and Helm validates before dropping nulls.
		additionalProperties: type: "null"
		properties: (key): {
			type:                 "object"
			required:             fields
			additionalProperties: true
			properties: {for f in fields {(f): type: "string"}}
		}
	}
}

_helmSecretKeyRef: (_helmRef & {key: "secretKeyRef", fields: ["name", "key"]}).out
_helmConfigMapKeyRef: (_helmRef & {key: "configMapKeyRef", fields: ["name", "key"]}).out
_helmFieldRef: (_helmRef & {key: "fieldRef", fields: ["fieldPath"]}).out
_helmResourceFieldRef: (_helmRef & {key: "resourceFieldRef", fields: ["resource"]}).out

// An optional input set to null in an overlay is unset.
_helmNull: {type: "null", description: "Unset."}

// Supplied at runtime by an injector (SPEC §4.5.1).
_helmProvider: {type: "string", pattern: "^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$"}

// Pod metadata for the injector (SPEC §4.5.2). Keys and values may hold
// placeholders such as {input}, so the schema checks only that values are
// strings; the library chart checks the expanded keys and values, and
// fails on an undefined placeholder or a conflict. A null value is
// dropped, so an overlay can remove a key.
_helmPodAnnotations: {type: "object", additionalProperties: type: ["string", "null"], ...}
_helmPodLabels: {type: "object", additionalProperties: type: ["string", "null"], ...}
_helmPodMetadata: {
	podAnnotations: _helmPodAnnotations & {description: "Pod annotations the injector needs; {input}, and for a file {path}, {dir} and {file}, are expanded."}
	podLabels: _helmPodLabels & {description: "Pod labels the injector needs; placeholders as for podAnnotations."}
}
_helmInjectedOf: {
	props: {...}
	out: {
		type: "object"
		required: ["injected"]
		additionalProperties: type: "null"
		properties: injected: {
			type: "object"
			required: ["provider"]
			additionalProperties: false
			properties: props
		}
	}
}
_helmInjected: (_helmInjectedOf & {props: {provider: _helmProvider, ref: {type: "string", minLength: 1}, _helmPodMetadata}}).out
_helmInjectedNoRef: (_helmInjectedOf & {props: {provider: _helmProvider, _helmPodMetadata}}).out

#HelmFile: {
	file: #File
	out: {...}

	let F = file
	let source = {
		key: string
		props: {...}
		required: [...string]
		out: {
			type: "object"
			"required": [key]
			additionalProperties: type: "null"
			properties: (key): {
				type:                 "object"
				additionalProperties: false
				properties:           props
				if len(required) > 0 {"required": required}
			}
		}
	}
	let name = {type: "string", minLength: 1}
	let duration = {type: "string", pattern: "^([0-9]+(ns|us|ms|s|m|h))+$"}
	let stringList = {type: "array", items: type: "string"}

	// Fields the platform tooling resolves from the cluster (SPEC §4.6.1).
	let resolvedSecret = {type: {type: "string"}, keys: stringList}
	let resolvedCertificate = {
		dnsNames: stringList
		privateKey: {type: "object", additionalProperties: false, properties: algorithm: enum: ["RSA", "ECDSA", "Ed25519"]}
		"duration":  duration
		renewBefore: duration
	}

	let inlineText = {
		type: "object"
		required: ["inline"]
		additionalProperties: type: "null"
		properties: inline: {
			type: "string"
			if F.type == "text" {
				if F.pattern != _|_ {pattern: F.pattern}
				if F.minLength != _|_ {minLength: F.minLength}
				if F.maxLength != _|_ {maxLength: F.maxLength}
			}
		}
	}

	// A config file's content may be written as data in the values file;
	// it is then checked against the file's own schema.
	let inlineData = {
		type: "object"
		required: ["inline"]
		additionalProperties: type: "null"
		properties: inline: [if F.type == "config" && F.schema != _|_ {F.schema}, {type: ["object", "array"]}][0]
	}

	let configMap = (source & {key: "configMap", props: {"name": name, key: name}, required: ["name", "key"]}).out
	let secretSingle = (source & {key: "secret", props: {"name": name, key: name, resolvedSecret}, required: ["name", "key"]}).out
	let secretDir = (source & {key: "secret", props: {"name": name, resolvedSecret}, required: ["name"]}).out
	let certificate = (source & {key: "certificate", props: {"name": name, secretName: name, resolvedCertificate}, required: ["name", "secretName"]}).out
	let csi = (source & {key: "csi", props: {secretProviderClass: name, driver: name}, required: ["secretProviderClass"]}).out
	let image = (source & {key: "image", props: {reference: name, pullPolicy: enum: ["Always", "IfNotPresent", "Never"]}, required: ["reference"]}).out
	let injectedFile = (source & {key: "injected", props: {provider: _helmProvider, _helmPodMetadata}, required: ["provider"]}).out

	out: {
		description: F.description
		oneOf: [
			if F.type == "tls" {secretDir},
			if F.type == "tls" {certificate},
			if F.type != "tls" {secretSingle},
			csi,
			injectedFile,
			if !F.secret && F.type != "binary" {inlineText},
			if !F.secret && F.type == "config" {inlineData},
			if !F.secret {configMap},
			if !F.secret {image},
			if !F.required {_helmNull},
		]
	}
}
