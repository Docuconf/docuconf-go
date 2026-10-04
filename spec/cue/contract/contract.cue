// Package contract is the docuconf environment contract meta-schema.
//
// An application's SDK emits a #Contract describing every environment
// variable it reads. The platform validates the values it intends to
// inject with #Validate, then turns them into a Kubernetes env list
// with #Render.
package contract

import (
	"list"
	"strings"
	"time"
)

// #Contract is the document every language SDK emits.
#Contract: {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "EnvContract"
	metadata: {
		// Service name, as a DNS label so it can name Kubernetes objects.
		name: =~"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$"
		// Application version the contract was exported from, e.g. a git SHA or semver.
		appVersion?: string
		generator: {
			language: "go" | "typescript" | "ruby" | "dotnet" | "python" | "java"
			sdk:      string
			version:  string
		}
	}
	vars: [N=#EnvName]: #Var & {name: N}
}

#EnvName: =~"^[A-Z][A-Z0-9_]*$"

#Common: {
	name:        #EnvName
	description: strings.MinRunes(5)
	required:    *false | bool
	secret:      *false | bool
	group?:      string
	examples?: [...string]
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

#Var: #StringVar | #IntVar | #FloatVar | #BoolVar | #DurationVar | #URLVar | #EnumVar | #ListVar

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

#Duration: =~"^([0-9]+(ns|us|ms|s|m|h))+$"

#DurationVar: close({
	#Common
	type:     "duration"
	default?: #Duration
	min?:     #Duration
	max?:     #Duration
})

#URLVar: close({
	#Common
	type:     "url"
	default?: string
	schemes?: [string, ...string]
})

#EnumVar: close({
	#Common
	type: "enum"
	values: [string, ...string]
	default?: or(values)
})

#ListVar: close({
	#Common
	type:      "list"
	items:     "string" | "int"
	separator: *"," | string
	minItems?: int & >=0
	maxItems?: int & >=0
	default?: [...]
})

// #SecretRef is the only accepted value for a secret variable. The
// platform never puts secret material in the values document.
#SecretRef: close({
	secretKeyRef: {
		name: string
		key:  string
	}
})

// #Check binds one contract variable to the value the platform supplies
// and fails if the value violates the variable's constraints.
#Check: {
	var:   #Var
	value: _

	if var.secret {
		value: #SecretRef
	}
	if !var.secret {
		if var.type == "string" {
			value: string
			if var.minLength != _|_ {value: strings.MinRunes(var.minLength)}
			if var.maxLength != _|_ {value: strings.MaxRunes(var.maxLength)}
			if var.pattern != _|_ {value: =~var.pattern}
		}
		if var.type == "int" {
			value: int
			if var.min != _|_ {value: >=var.min}
			if var.max != _|_ {value: <=var.max}
		}
		if var.type == "float" {
			value: number
			if var.min != _|_ {value: >=var.min}
			if var.max != _|_ {value: <=var.max}
		}
		if var.type == "bool" {
			value: bool
		}
		if var.type == "duration" {
			value: #Duration
			if var.min != _|_ {
				atLeastMin: true & time.ParseDuration(value) >= time.ParseDuration(var.min)
			}
			if var.max != _|_ {
				atMostMax: true & time.ParseDuration(value) <= time.ParseDuration(var.max)
			}
		}
		if var.type == "url" {
			value: =~"^[a-zA-Z][a-zA-Z0-9+.-]*://[^\\s]+$"
			if var.schemes != _|_ {
				value: =~"^(\(strings.Join(var.schemes, "|")))://"
			}
		}
		if var.type == "enum" {
			value: or(var.values)
		}
		if var.type == "list" {
			if var.items == "string" {value: [...string]}
			if var.items == "int" {value: [...int]}
			if var.minItems != _|_ {value: list.MinItems(var.minItems)}
			if var.maxItems != _|_ {value: list.MaxItems(var.maxItems)}
		}
	}
}

// #Validate unifies a contract with the values the platform will inject.
// Required variables must be present, unknown variables are rejected,
// and every value must satisfy its variable's constraints.
#Validate: {
	contract: #Contract
	values: close({
		for n, v in contract.vars {
			if v.required {(n)!: _}
			if !v.required {(n)?: _}
		}
	})
	checks: {
		for n, v in contract.vars if values[n] != _|_ {
			(n): #Check & {var: v, value: values[n]}
		}
	}
}

// #Render turns validated values into a Kubernetes container env list,
// using the canonical string encoding every SDK parses.
#Render: {
	contract: #Contract
	values: [string]: _
	env: [
		for n, v in contract.vars if values[n] != _|_ {
			let x = values[n]
			if v.secret {
				name:      n
				valueFrom: x
			}
			if !v.secret {
				name: n
				if v.type == "list" {
					value: strings.Join([for i in x {"\(i)"}], v.separator)
				}
				if v.type != "list" {
					value: "\(x)"
				}
			}
		},
	]
}
