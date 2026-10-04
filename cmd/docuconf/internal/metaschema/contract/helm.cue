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
	let _requiredVars = list.SortStrings([for n, v in contract.vars if v.required && _profile[n] == _|_ {n}])
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
					properties: {for n, v in contract.vars {(n): (#HelmVar & {var: v}).out}}
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
			}
			let req = list.Concat([[if len(_requiredVars) > 0 {"values"}], [if len(_requiredFiles) > 0 {"files"}]])
			if len(req) > 0 {required: req}
		}
		if len(_requiredVars) > 0 || len(_requiredFiles) > 0 {required: ["docuconf"]}
	}
}

#HelmVar: {
	var: #Var
	out: {...}

	let literal = {
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
			if var.minItems != _|_ {minItems: var.minItems}
			if var.maxItems != _|_ {maxItems: var.maxItems}
		}
		if var.type == "json" && var.schema != _|_ {var.schema}
	}

	if var.secret {
		out: _helmSecretKeyRef & {description: "\(var.description) (secret: supply a secretKeyRef)"}
	}
	if !var.secret {
		out: {
			description: var.description
			anyOf: [
				literal,
				if var.type != "list" {_helmConfigMapKeyRef},
				if var.type == "string" {_helmFieldRef},
				if var.type == "int" {_helmResourceFieldRef},
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
		additionalProperties: false
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
			additionalProperties: false
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
		additionalProperties: false
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
		additionalProperties: false
		properties: inline: [if F.type == "config" && F.schema != _|_ {F.schema}, {type: ["object", "array"]}][0]
	}

	let configMap = (source & {key: "configMap", props: {"name": name, key: name}, required: ["name", "key"]}).out
	let secretSingle = (source & {key: "secret", props: {"name": name, key: name, resolvedSecret}, required: ["name", "key"]}).out
	let secretDir = (source & {key: "secret", props: {"name": name, resolvedSecret}, required: ["name"]}).out
	let certificate = (source & {key: "certificate", props: {"name": name, secretName: name, resolvedCertificate}, required: ["name", "secretName"]}).out
	let csi = (source & {key: "csi", props: {secretProviderClass: name, driver: name}, required: ["secretProviderClass"]}).out
	let image = (source & {key: "image", props: {reference: name, pullPolicy: enum: ["Always", "IfNotPresent", "Never"]}, required: ["reference"]}).out

	out: {
		description: F.description
		oneOf: [
			if F.type == "tls" {secretDir},
			if F.type == "tls" {certificate},
			if F.type != "tls" {secretSingle},
			csi,
			if !F.secret && F.type != "binary" {inlineText},
			if !F.secret && F.type == "config" {inlineData},
			if !F.secret {configMap},
			if !F.secret {image},
		]
	}
}
