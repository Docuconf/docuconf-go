// Package contract is the docuconf configuration contract meta-schema.
//
// An application's SDK emits a #Contract describing every input it reads:
// environment variables (vars) and files (files.cue). The platform
// validates what it intends to supply with #Validate, then turns it into
// Kubernetes env entries, volumes, mounts and pod metadata with #Render.
package contract

import (
	"encoding/json"
	"list"
	"path"
	"regexp"
	"strings"
	"time"
)

// #Contract is the document every language SDK emits.
#Contract: {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		// Service name, as a DNS label so it can name Kubernetes objects.
		name: =~"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$"
		// Application version the contract was exported from, e.g. a git SHA or semver.
		appVersion?: string
		generator: {
			language: "go" | "typescript" | "ruby" | "dotnet" | "python" | "java" | "kotlin" | "rust" | "swift" | "elixir" | "gleam" | "cpp" | "php" | "cobol"
			sdk:      string
			version:  string
		}
	}
	vars: [N=#EnvName]: #Var & {name: N}
	files?: [N=#InputName]: #File & {name: N}
	// Config files the platform may mount over the app's own (overlays.cue).
	overlays?: [N=#InputName]: #Overlay & {name: N}

	// Profiles describe config files baked into the image and selected by
	// an environment variable at runtime: appsettings.{Environment}.json
	// in .NET, application-{profile}.yml in Spring, per-environment YAML
	// in Rails. Values from the always-loaded base file are ordinary
	// defaults; values from a profile file apply only when that profile
	// is selected.
	profiles?: {
		selector: #EnvName // e.g. DOTNET_ENVIRONMENT
		default:  string   // profile in effect when the selector is unset
		defaults: [string]: [#EnvName]: _
	}

	// Every profile default must name a declared variable and satisfy
	// its constraints. A secret can never have one, because #Check only
	// accepts a secret reference.
	if profiles != _|_ {
		// The selector is itself an environment variable the platform sets,
		// and profiles can only set declared variables. vars has a name
		// pattern, so a plain lookup of an unknown name is merely
		// incomplete, not an error; test membership explicitly.
		_declared: "\(profiles.selector)": true & vars[profiles.selector] != _|_
		for p, m in profiles.defaults for n, _ in m {
			_declared: "\(p)/\(n)": true & vars[n] != _|_
		}
		_profileChecks: {
			for p, m in profiles.defaults for n, x in m if vars[n] != _|_ {
				"\(p)/\(n)": #Check & {var: vars[n], value: x}
			}
		}
	}

	if overlays != _|_ {
		for n, o in overlays {
			let dir = path.Dir(o.path, path.Unix)
			_mountDirs: "\(dir)": "overlay \(n)"
			_mountNotReserved: "overlay \(n)": true & !list.Contains(#ReservedDirs, dir)
		}
	}

	if files != _|_ {
		for n, f in files {
			let mountDir = [if f.type == "tls" {f.path}, path.Dir(f.path, path.Unix)][0]

			// Two inputs mounted at the same directory would hide each other.
			_mountDirs: "\(mountDir)": n
			_mountNotReserved: "\(n)": true & !list.Contains(#ReservedDirs, mountDir)
			if f.pathEnv != _|_ {
				_pathEnvs: "\(f.pathEnv)":         n
				_pathEnvNotInVars: "\(f.pathEnv)": true & vars[f.pathEnv] == _|_
			}
			if f.type == "keystore" && f.passwordVar != _|_ {
				_passwordIsSecretVar: "\(f.passwordVar)": true & vars[f.passwordVar] != _|_ && vars[f.passwordVar].secret == true
			}
		}
	}
}

#EnvName: =~"^[A-Z][A-Z0-9_]*$"

// #Details is the optional long-form documentation of an input, in
// CommonMark: not blank, and at most 4000 characters (Unicode code points).
#Details: strings.MaxRunes(4000) & =~"[^\\s]"

#Common: {
	name:        #EnvName
	description: strings.MinRunes(5)
	// Markdown for docs only: why the input exists and when to change it.
	// Never read at runtime.
	details?: #Details
	required:    *false | bool
	secret:      *false | bool
	group?:      string
	examples?: [...string]
	// The app's own configuration key, where it differs from the env
	// name: "Orders:CheckoutTimeout" in .NET, "orders.checkout-timeout"
	// in Spring. Used for docs and for file-based rendering.
	configKey?: string
	deprecated?: {
		message:     string
		replacedBy?: #EnvName
	}
	// A required variable has no default: the platform must supply it.
	if required {
		default?: _|_
	}

	// Secrets never carry defaults or examples in the contract.
	if secret {
		default?:  _|_
		examples?: _|_
	}
}

#Var: #StringVar | #IntVar | #FloatVar | #BoolVar | #DurationVar | #URLVar | #EnumVar | #ListVar | #JSONVar

#StringVar: close({
	#Common
	type:       "string"
	default?:   string
	minLength?: int & >=0
	maxLength?: int & >=0
	pattern?:   string // RE2 syntax
})

#IntVar: close({
	#Common
	type:     "int"
	default?: int
	min?:     int
	max?:     int
})

#FloatVar: close({
	#Common
	type:     "float"
	default?: number
	min?:     number
	max?:     number
})

#BoolVar: close({
	#Common
	type:     "bool"
	default?: bool
})

// Durations are always written in Go syntax in contracts and platform
// values. The encoding says what the app's host library parses, and
// #Render converts to it.
#Duration: =~"^([0-9]+(ns|us|ms|s|m|h))+$"

// go:       1m30s     (Go time.ParseDuration)
// iso8601:  PT90S     (pydantic timedelta, ActiveSupport::Duration.parse, java.time.Duration in Spring Boot and Hoplite)
// seconds:  90        (plain number of seconds)
// timespan: 00:01:30  (.NET TimeSpan.Parse)
#DurationEncoding: "go" | "iso8601" | "seconds" | "timespan"

#DurationVar: close({
	#Common
	type:     "duration"
	encoding: *"go" | #DurationEncoding
	default?: #Duration
	min?:     #Duration
	max?:     #Duration
})

#URLVar: close({
	#Common
	type:     "url"
	default?: string
	schemes?: [string, ...string]
	// Characters (Unicode code points), as for a string's maxLength.
	maxLength?: int & >=0
})

#EnumVar: close({
	#Common
	type: "enum"
	values: [string, ...string]
	default?: or(values)
})

// csv:     a,b          (caarlos0/env, Spring Boot, anyway_config)
// json:    ["a","b"]    (pydantic-settings)
// indexed: NAME__0=a, NAME__1=b  (Microsoft.Extensions.Configuration)
#ListEncoding: "csv" | "json" | "indexed"

#ListVar: close({
	#Common
	type:     "list"
	items:    "string" | "int"
	encoding: *"csv" | #ListEncoding
	if encoding == "csv" {
		separator: *"," | string
	}
	minItems?: int & >=0
	maxItems?: int & >=0
	// Bounds on each item of an int list, so a list can carry the range its
	// host item type holds (a 32-bit int, a JavaScript number), as min and
	// max do for an int variable.
	itemMin?: int
	itemMax?: int
	if itemMin != _|_ || itemMax != _|_ {
		_itemBoundsOnIntItems: true & items == "int"
	}
	// Bounds on the length of each item of a string list, in characters
	// (Unicode code points), as minLength and maxLength for a string.
	itemMinLength?: int & >=0
	itemMaxLength?: int & >=0
	if itemMinLength != _|_ || itemMaxLength != _|_ {
		_itemLengthsOnStringItems: true & items == "string"
	}
	default?: [...]
})

// A structured value in one variable, sent as JSON. As with config files,
// `schema` is a JSON Schema generated from the app's own type.
#JSONVar: close({
	#Common
	type: "json"
	schema?: {...}
	// Characters (Unicode code points) of the value's wire form: the
	// compact JSON the platform renders, or the raw value the app receives.
	maxLength?: int & >=0
	default?:   _
})

// #SecretRef is the only accepted value for a secret variable. The
// platform never puts secret material in the values document.
#SecretRef: close({
	secretKeyRef: {
		name: string
		key:  string
	}
})

// #Injected is a value supplied at runtime by something other than the pod
// spec: a mutating webhook (Bank-Vaults), a wrapper process (op run) or an
// operator (the OpenTelemetry operator). The SDK validates the value when
// the process starts, after injection, so nothing about it is checked
// before deploy except the shape of the reference.
#Injected: close({injected: {
	// Who supplies the value, e.g. "bank-vaults", "otel-operator".
	provider: #Provider
	// The reference the injector resolves, rendered verbatim as the env
	// value, e.g. "vault:secret/data/db#url". Omitted when the injector
	// sets the variable itself; nothing is rendered then.
	ref?: string & !=""
	// Pod annotations and labels the injector needs (pod.cue, SPEC
	// §4.5.2), with {input} expanded to the variable's name.
	podAnnotations?: #PodAnnotations
	podLabels?:      #PodLabels
}})

#Provider: =~"^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$"

// Non-secret values the platform supplies by reference rather than as a
// literal. Their content is not known before deploy, so the SDK checks it
// at boot.
#ValueRef: #ConfigMapKeyRef | #FieldRef | #ResourceFieldRef

#ConfigMapKeyRef: close({configMapKeyRef: {
	name: string
	key:  string
}})

// The Downward API. Every field yields a string.
#FieldRef: close({fieldRef: fieldPath: "metadata.name" | "metadata.namespace" | "metadata.uid" | "spec.nodeName" | "spec.serviceAccountName" | "status.hostIP" | "status.podIP" | =~"^metadata\\.(labels|annotations)\\['[^']+'\\]$"})

// Container resources. Every field yields an integer.
#ResourceFieldRef: close({resourceFieldRef: {
	resource:       "limits.cpu" | "limits.memory" | "limits.ephemeral-storage" | "requests.cpu" | "requests.memory" | "requests.ephemeral-storage"
	divisor?:       string
	containerName?: string
}})

// #Check binds one contract variable to the value the platform supplies
// and fails if the value violates the variable's constraints.
#Check: {
	var:      #Var
	value:    _
	#schema?: _ // a json variable's JSON Schema, compiled to CUE by the toolchain

	// Constraints go on `literal`, a copy of value, so that whether value
	// is a reference can be decided from value without a cycle.
	let isRef = (value & #ValueRef) != _|_
	let isInjected = (value & #Injected) != _|_

	if var.secret {
		value: #SecretRef | #Injected
	}
	if isInjected {
		pod: #PodMetadata & {input: var.name, from: value.injected}
	}
	if isInjected && value.injected.ref != _|_ && var.type == "list" {
		// One env value cannot carry a list spread over NAME__0, NAME__1.
		injectedRefNotIndexed: true & var.encoding != "indexed"
	}
	if !var.secret && isRef {
		if value.fieldRef != _|_ {
			fieldRefIsString: true & var.type == "string"
		}
		if value.resourceFieldRef != _|_ {
			resourceFieldRefIsInt: true & var.type == "int"
		}
		listCannotBeRef: true & var.type != "list"
	}
	if !var.secret && !isRef && !isInjected {
		literal: value
		if var.type == "string" {
			literal: string
			if var.minLength != _|_ {literal: strings.MinRunes(var.minLength)}
			if var.maxLength != _|_ {literal: strings.MaxRunes(var.maxLength)}
			if var.pattern != _|_ {literal: =~var.pattern}
		}
		if var.type == "int" {
			literal: int
			if var.min != _|_ {literal: >=var.min}
			if var.max != _|_ {literal: <=var.max}
		}
		if var.type == "float" {
			literal: number
			if var.min != _|_ {literal: >=var.min}
			if var.max != _|_ {literal: <=var.max}
		}
		if var.type == "bool" {
			literal: bool
		}
		if var.type == "duration" {
			literal: #Duration
			if var.encoding != "go" {
				// Only the go encoding can express sub-millisecond durations.
				wholeMilliseconds: true & mod(time.ParseDuration(literal), 1000000) == 0
			}
			if var.min != _|_ {
				atLeastMin: true & time.ParseDuration(literal) >= time.ParseDuration(var.min)
			}
			if var.max != _|_ {
				atMostMax: true & time.ParseDuration(literal) <= time.ParseDuration(var.max)
			}
		}
		if var.type == "url" {
			literal: =~"^[a-zA-Z][a-zA-Z0-9+.-]*://[^\\s]+$"
			if var.schemes != _|_ {
				literal: =~"^(\(strings.Join([for x in var.schemes {regexp.QuoteMeta(x)}], "|")))://"
			}
			if var.maxLength != _|_ {literal: strings.MaxRunes(var.maxLength)}
		}
		if var.type == "enum" {
			literal: or(var.values)
		}
		if var.type == "json" && #schema != _|_ {
			literal: #schema
		}
		if var.type == "json" && var.maxLength != _|_ {
			// Measured on the compact JSON #Render writes.
			withinMaxLength: json.Marshal(literal) & strings.MaxRunes(var.maxLength)
		}
		if var.type == "list" {
			if var.items == "string" {
				literal: [...string]
				if var.itemMinLength != _|_ {literal: [...strings.MinRunes(var.itemMinLength)]}
				if var.itemMaxLength != _|_ {literal: [...strings.MaxRunes(var.itemMaxLength)]}
			}
			if var.items == "int" {
				literal: [...int]
				if var.itemMin != _|_ {literal: [...>=var.itemMin]}
				if var.itemMax != _|_ {literal: [...<=var.itemMax]}
			}
			if var.minItems != _|_ {literal: list.MinItems(var.minItems)}
			if var.maxItems != _|_ {literal: list.MaxItems(var.maxItems)}
		}
	}
}

// #Validate unifies a contract with what the platform will supply.
// Unknown variables and inputs are rejected, every value must satisfy its
// constraints, and every required variable must be set, directly or by
// the selected profile, and every required file input given a source.
#Validate: {
	contract: #Contract
	values: close({
		for n, _ in contract.vars {(n)?: _}
		// Pod metadata shared by every injector (SPEC §4.5.2). Variable
		// names are upper case, so these never collide with one.
		podAnnotations?: #PodAnnotations
		podLabels?:      #PodLabels
	})
	// The source of each file input, keyed by input name.
	files: close({
		if contract.files != _|_ {
			for n, _ in contract.files {(n)?: #FileSource}
		}
	})
	// Values the platform writes into a config-file overlay instead of the
	// environment, keyed by overlay name, then variable name.
	overlays: close({
		if contract.overlays != _|_ {
			for o, _ in contract.overlays {
				(o)?: close({for n, _ in contract.vars {(n)?: _}})
			}
		}
	})
	// JSON Schemas from the contract, compiled to CUE by the toolchain
	// (cuelang.org/go/encoding/jsonschema), keyed by variable or input name.
	#schemas: [string]: _

	overlayChecks: {
		for o, m in overlays for n, x in m {
			let v = contract.vars[n]
			"\(o)/\(n)": {
				// Overlays are ConfigMaps, never a place for secret material.
				notSecret: true & !v.secret
				// A file holds values, not Kubernetes or injector references.
				literalOnly: true & (x & #ValueRef) == _|_ && (x & #SecretRef) == _|_ && (x & #Injected) == _|_
				// The value goes at the variable's configKey in the file.
				hasConfigKey: true & v.configKey != _|_
				if v.configKey != _|_ {
					withinKeyDepth: true & len(strings.Split(v.configKey, contract.overlays[o].keySeparator)) <= #MaxKeyDepth
				}
				// The profile selector picks which files load, so it cannot come from one.
				if contract.profiles != _|_ {
					notProfileSelector: true & contract.profiles.selector != n
				}
				if !v.secret && (x & #ValueRef) == _|_ && (x & #Injected) == _|_ {
					value: #CheckOverlayValue & {var: v, value: x, if #schemas[n] != _|_ {#schema: #schemas[n]}}
				}
			}
		}
	}
	// A variable comes from one place: the environment or one overlay. The
	// environment would silently win over the overlay, so both is an error.
	_suppliedBy: {
		for n, _ in values if contract.vars[n] != _|_ {(n): "env"}
		for o, m in overlays for n, _ in m {(n): "overlay \(o)"}
	}

	checks: {
		for n, v in contract.vars if values[n] != _|_ {
			(n): #Check & {var: v, value: values[n], if #schemas[n] != _|_ {#schema: #schemas[n]}}
		}
	}
	fileChecks: {
		if contract.files != _|_ {
			for n, f in contract.files if files[n] != _|_ {
				(n): #CheckFile & {file: f, source: files[n], if #schemas[n] != _|_ {#schema: #schemas[n]}}
			}
		}
	}

	// Pod annotations and labels: the shared ones are checked here, each
	// injected source's in its own check (checks.NAME.pod,
	// fileChecks.NAME.pod), and no two sources may disagree on a key.
	podChecks: {
		shared: #PodMetadata & {from: {
			if values.podAnnotations != _|_ {podAnnotations: values.podAnnotations}
			if values.podLabels != _|_ {podLabels: values.podLabels}
		}}
		#PodMetadataConflicts & {sources: {
			"(shared)": shared.out
			for n, c in checks if c.pod != _|_ {(n): c.pod.out}
			for n, c in fileChecks if c.pod != _|_ {(n): c.pod.out}
		}}
	}

	// Kept separate from values: making a field of values required based
	// on another field of values (the profile selector) is a cycle.
	missingRequired: close({
		for n, v in contract.vars
		if v.required && values[n] == _|_ && _fromProfile[n] == _|_ && _inOverlay[n] == _|_ {
			(n): "required, and not set by the platform or the selected profile"
		}
		if contract.files != _|_ {
			for n, f in contract.files if f.required && files[n] == _|_ {
				(n): "required file input, and no source given"
			}
		}
	})
	missingRequired: close({})

	_inOverlay: {for o, m in overlays for n, _ in m {(n): true}}

	// A required variable is satisfied by the selected profile's file.
	_fromProfile: {...}
	if contract.profiles != _|_ {
		let P = contract.profiles
		let selected = [if values[P.selector] != _|_ {values[P.selector]}, P.default][0]
		if P.defaults[selected] != _|_ {
			_fromProfile: P.defaults[selected]
		}
	}
}

// #Render turns validated values into the container's env entries and,
// for file inputs, the volumes, mounts and ConfigMaps that deliver them.
// restartTriggers lists the objects whose changes must roll the pods,
// for inputs the app reads only at startup. podAnnotations and podLabels
// are what injected sources ask to have on the pod template (SPEC §4.5.2).
#Render: {
	contract: #Contract
	values: [string]: _
	files: [string]:  #FileSource
	overlays: [string]: [string]: _

	let _overlays = [
		if contract.overlays != _|_ for o, ov in contract.overlays if overlays[o] != _|_ {
			#RenderOverlay & {service: contract.metadata.name, overlay: ov, vars: contract.vars, values: overlays[o]}
		},
	]

	let _files = [
		if contract.files != _|_ for n, f in contract.files if files[n] != _|_ {
			#RenderFile & {service: contract.metadata.name, name: n, file: f, source: files[n]}
		},
	]

	env: list.FlattenN([
		for n, v in contract.vars if values[n] != _|_ {
			(#RenderVar & {name: n, var: v, value: values[n]}).out
		},
		for r in _files {r.env},
	], 1)
	volumes: list.Concat([list.FlattenN([for r in _files {r.volumes}], 1), [for r in _overlays {r.volume}]])
	volumeMounts: list.Concat([list.FlattenN([for r in _files {r.volumeMounts}], 1), [for r in _overlays {r.volumeMount}]])
	configMaps: list.Concat([list.FlattenN([for r in _files {r.configMaps}], 1), [for r in _overlays {r.configMap}]])
	restartTriggers: list.FlattenN([for r in _files {r.restartTriggers}], 1)

	// Shared first, then variables and file inputs in contract order. A key
	// set to different values by two sources fails here as a conflict;
	// #Validate names the two sources (podChecks).
	let _pod = [
		(#PodMetadata & {from: {
			if values.podAnnotations != _|_ {podAnnotations: values.podAnnotations}
			if values.podLabels != _|_ {podLabels: values.podLabels}
		}}).out,
		for n, v in contract.vars if values[n] != _|_ if (values[n] & #Injected) != _|_ {
			(#PodMetadata & {input: n, from: values[n].injected}).out
		},
		if contract.files != _|_ for n, f in contract.files if files[n] != _|_ if files[n].injected != _|_ {
			(#PodMetadata & {input: n, file: f, from: files[n].injected}).out
		},
	]
	podAnnotations: {for m in _pod for k, v in m.annotations {(k): v}}
	podLabels: {for m in _pod for k, v in m.labels {(k): v}}
}

#RenderVar: {
	name:  string
	var:   #Var
	value: _
	out: [...{...}]

	// Aliases: inside {name: ..., value: ...} the bare names would refer
	// to the new struct's own fields.
	let N = name
	let V = value

	let isRef = (V & #ValueRef) != _|_
	let isInjected = (V & #Injected) != _|_

	if isInjected {
		out: [if V.injected.ref != _|_ {{name: N, value: strings.Replace(V.injected.ref, "$", "$$", -1)}}]
	}
	if !isInjected && (var.secret || isRef) {
		out: [{name: N, valueFrom: V}]
	}

	// Kubernetes expands $(VAR) in env values and reduces $$ to $, so
	// every literal $ is doubled to arrive unchanged.
	let esc = {
		in:  string
		out: strings.Replace(in, "$", "$$", -1)
	}

	if !var.secret && !isRef && !isInjected {
		if var.type == "json" {
			out: [{name: N, value: (esc & {in: json.Marshal(V)}).out}]
		}
		if var.type == "list" {
			if var.encoding == "csv" {
				out: [{name: N, value: (esc & {in: strings.Join([for i in V {"\(i)"}], var.separator)}).out}]
			}
			if var.encoding == "json" {
				out: [{name: N, value: (esc & {in: json.Marshal(V)}).out}]
			}
			if var.encoding == "indexed" {
				out: [for i, x in V {name: "\(N)__\(i)", value: (esc & {in: "\(x)"}).out}]
			}
		}
		if var.type == "duration" {
			out: [{name: N, value: (#RenderDuration & {in: V, encoding: var.encoding}).out}]
		}
		if var.type != "list" && var.type != "duration" && var.type != "json" {
			out: [{name: N, value: (esc & {in: "\(V)"}).out}]
		}
	}
}

#RenderDuration: {
	in:       #Duration
	encoding: #DurationEncoding
	out:      string

	let ms = div(time.ParseDuration(in), 1000000)
	let secs = div(ms, 1000)
	let frac = mod(ms, 1000)
	let fracDigits = strings.TrimRight([
		if frac < 10 {"00\(frac)"},
		if frac < 100 {"0\(frac)"},
		"\(frac)",
	][0], "0")
	let fracStr = [if frac == 0 {""}, ".\(fracDigits)"][0]
	let pad = {
		n: int
		out: [if n < 10 {"0\(n)"}, "\(n)"][0]
	}
	let days = div(secs, 86400)
	let hh = (pad & {n: div(mod(secs, 86400), 3600)}).out
	let mm = (pad & {n: div(mod(secs, 3600), 60)}).out
	let ss = (pad & {n: mod(secs, 60)}).out

	if encoding == "go" {out: in}
	if encoding == "seconds" {out: "\(secs)\(fracStr)"}
	if encoding == "iso8601" {out: "PT\(secs)\(fracStr)S"}
	if encoding == "timespan" {
		out: [if days > 0 {"\(days)."}, ""][0] + "\(hh):\(mm):\(ss)\(fracStr)"
	}
}
