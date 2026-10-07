// Package docs is the schema of the docuconf docs model (SPEC §14).
//
// `docuconf docs` builds a #DocsModel from a contract, and its renderers
// (Markdown for developers, a rules file for agents) read only the model.
// Third parties (a website, an MCP server, a Backstage plugin) can render
// from the same JSON document, so every fact a renderer needs is in it,
// including pre-phrased sentences for constraints, wire formats, sources
// and boot errors, so that every renderer phrases them the same way.
package docs

import "strings"

#DocsModel: close({
	apiVersion: "docs.docuconf.dev/v1alpha1"
	kind:       "ConfigDocs"
	service: close({
		// The contract's metadata.name.
		name:        =~"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$"
		appVersion?: string
		generator: close({
			language: string
			sdk:      string
			version:  string
		})
	})
	// The profile selector and the profiles with defaults (SPEC §4.4).
	profiles?: close({
		selector: #EnvName
		default:  string
		names: [...string]
	})
	// Config-file overlays the platform may write values into (SPEC §4.7).
	overlays?: [...close({
		name:         #InputName
		format:       "json" | "yaml" | "toml"
		path:         string
		keySeparator: string
		reload:       "restart" | "watch"
	})]
	// Inputs by group: the ungrouped inputs first (name ""), then the named
	// groups in code point order. Within a group, variables, then files,
	// each sorted by name. A group always has at least one input.
	groups: [...#Group]
	// The boot error codes that apply to at least one input, in the order
	// of SPEC §11.2 item 5.
	errors: [...#ErrorInfo]
})

#EnvName:   =~"^[A-Z][A-Z0-9_]*$"
#InputName: =~"^[a-z]([-a-z0-9]{0,40}[a-z0-9])?$"

#Group: close({
	// The contract's group, or "" for inputs without one.
	name: string
	// The heading renderers show: the name, or "General" for "".
	title: string & !=""
	inputs: [#Input, ...#Input]
	for i in inputs {
		_sameGroup: "\(i.kind)/\(i.name)": true & ((i.group == _|_ && name == "") || (i.group != _|_ && i.group == name))
	}
})

#Input: #VarInput | #FileInput

#InputCommon: {
	// A human label for the type: "integer", "list of strings",
	// "YAML config file", "TLS key pair".
	typeLabel: string & !=""
	// Absent when the input has no group (or an empty one).
	group?:      string & !=""
	required:    bool
	secret:      bool
	description: strings.MinRunes(5)
	// CommonMark, from the contract. Renderers demote its headings (§14.4).
	details?: strings.MaxRunes(4000) & =~"[^\\s]"
	deprecated?: close({
		message:     string
		replacedBy?: string
	})
	constraints: [...#Constraint]
	// Where the platform may get the value or content from.
	sources: [#Source, ...#Source]
	// Boot error codes the SDK may report for this input. Empty for an
	// optional, unconstrained string, which any value satisfies.
	errors: [...#Code]

	// A secret never has a value in the docs (SPEC §6).
	if secret {
		default?:         _|_
		defaultEnv?:      _|_
		examples?:        _|_
		profileDefaults?: _|_
	}
}

#VarInput: {
	#InputCommon
	kind: "var"
	name: #EnvName
	type: "string" | "int" | "float" | "bool" | "duration" | "url" | "enum" | "list" | "json"
	// The contract's default, as a typed platform value.
	default?: _
	// The default as the process environment holds it, in the wire
	// format: one entry, or one per item for an indexed list.
	defaultEnv?: [...close({name: #EnvName, value: string})]
	examples?: [...string]
	configKey?: string
	// The variable selects the profile (SPEC §4.4).
	profileSelector?: true
	// Defaults from the app's profile files, by profile name.
	profileDefaults?: [...close({profile: string, value: _})]
	wire: close({
		// list and duration only: the encoding the app parses.
		encoding?: string
		// csv lists only.
		separator?: string
		// How the value is written in the process environment.
		text: string & !=""
		// How the value is written in a platform values file.
		platform: string & !=""
	})
}

#FileInput: {
	#InputCommon
	kind: "file"
	name: #InputName
	type: "config" | "tls" | "caBundle" | "keystore" | "text" | "binary"
	file: close({
		path:     string
		pathEnv?: #EnvName
		// config: json, yaml or toml; keystore: pkcs12 or jks.
		format?:  string
		reload:   "restart" | "watch"
		maxSize?: int & >0
		// What the file holds, in plain words.
		contents: string & !=""
		// What happens when the source changes, in plain words.
		reloadText: string & !=""
	})
}

// A constraint as data and as a phrase. rule names the check; params
// holds the contract fields it comes from, under their contract names;
// text is a sentence fragment in CommonMark inline syntax, such as
// "between 1 and 65535" or "at most 120 characters (Unicode code points)".
#Constraint: close({
	rule: "range" | "length" | "pattern" | "schemes" | "values" | "itemCount" | "itemRange" | "itemLength" |
		"schema" | "maxSize" | "dnsNames" | "keyAlgorithms" | "minRemaining" | "requireCA" | "minCertificates" | "passwordVar"
	params: {[string]: _}
	text: string & !=""
})

// A source the platform may use: a value source for a variable
// (SPEC §4.5, §4.7, §6) or a file source (SPEC §4.6.1).
#Source: close({
	kind: "literal" | "configMapKeyRef" | "fieldRef" | "resourceFieldRef" | "secretKeyRef" | "injected" | "overlay" |
		"inline" | "configMap" | "secret" | "certificate" | "csi" | "image"
	// overlay only: the overlay's name.
	overlay?: #InputName
	// What is particular to this input, such as an overlay's key.
	note?: string & !=""
	// What the kind means; the same for every input.
	text: string & !=""
})

#Code: "missing_required" | "invalid_type" | "out_of_range" | "pattern_mismatch" | "not_in_enum" | "invalid_scheme" |
	"too_few_items" | "too_many_items" | "file_missing" | "file_unreadable" | "file_too_large" | "file_malformed" |
	"schema_mismatch" | "certificate_invalid" | "certificate_expiring" | "certificate_name_mismatch" | "key_mismatch" |
	"keystore_unreadable"

#ErrorInfo: close({
	code:    #Code
	meaning: string & !=""
	fix:     string & !=""
})
