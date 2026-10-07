package contract

import (
	"path"
	"strings"
)

// Pod metadata for injectors (SPEC §4.5.2). An injector is usually turned
// on, and told what to do, by annotations or labels on the pod: the Vault
// Agent injector reads vault.hashicorp.com/*, Bank-Vaults
// vault.security.banzaicloud.io/*, the OpenTelemetry operator
// instrumentation.opentelemetry.io/inject-*. An injected source in the
// platform's values or files document may carry the ones it needs, and
// #Render merges them into podAnnotations and podLabels. docuconf does not
// know any injector: keys and values are passed through, after the
// placeholders below are expanded.
//
// This is platform-side only. The contract does not change, because the
// same image runs under different injectors in different clusters.

// #PodAnnotations and #PodLabels as written in a values or files document,
// before placeholders are expanded. The expanded result is checked by
// #PodMetadata.
#PodAnnotations: [string]: string
#PodLabels: [string]: string

// A Kubernetes qualified name, the form of every annotation and label key:
// an optional DNS-subdomain prefix (at most 253 characters) and "/", then
// a name of at most 63 characters.
#QualifiedName: =~"^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$" & =~"^([^/]{1,253}/)?[^/]+$"

// A label value: empty, or at most 63 characters of the name alphabet.
// Annotation values may be any string.
#LabelValue: =~"^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$"

// Placeholders, expanded in keys and values. A variable defines {input};
// a file input defines all four; the values document's shared
// podAnnotations and podLabels define none. Using one a source does not
// define is an error, so a typo cannot reach the pod.
//
//   {input}  the variable or file input's name
//   {path}   the file input's path
//   {dir}    the directory it is mounted at: the path itself for tls,
//            the parent directory otherwise
//   {file}   the last element of the path
#Placeholder: "{input}" | "{path}" | "{dir}" | "{file}"

_placeholderRe: "\\{(input|path|dir|file)\\}"

// #PodMetadata expands and checks the pod annotations and labels of one
// source: an injected variable (input set), an injected file input (input
// and file set), or the values document's shared ones (neither set).
#PodMetadata: {
	input?: string
	file?:  #File
	from: {
		podAnnotations?: #PodAnnotations
		podLabels?:      #PodLabels
		...
	}

	let defined = {
		if input != _|_ {"{input}": input}
		if file != _|_ {
			"{path}": file.path
			"{dir}": [if file.type == "tls" {file.path}, path.Dir(file.path, path.Unix)][0]
			"{file}": path.Base(file.path, path.Unix)
		}
	}
	// Names and paths never contain braces, so the order of replacement
	// cannot matter.
	let expand = {
		in: string
		let s1 = [if defined["{input}"] != _|_ {strings.Replace(in, "{input}", defined["{input}"], -1)}, in][0]
		let s2 = [if defined["{path}"] != _|_ {strings.Replace(s1, "{path}", defined["{path}"], -1)}, s1][0]
		let s3 = [if defined["{dir}"] != _|_ {strings.Replace(s2, "{dir}", defined["{dir}"], -1)}, s2][0]
		out: [if defined["{file}"] != _|_ {strings.Replace(s3, "{file}", defined["{file}"], -1)}, s3][0]
	}

	out: {
		annotations: {
			if from.podAnnotations != _|_ for k, v in from.podAnnotations {
				"\((expand & {in: k}).out)": (expand & {in: v}).out
			}
		}
		labels: {
			if from.podLabels != _|_ for k, v in from.podLabels {
				"\((expand & {in: k}).out)": (expand & {in: v}).out
			}
		}
	}

	// Checks on the expanded keys and values, keyed by the expanded key.
	undefinedPlaceholder: {
		for k, v in out.annotations {"podAnnotations \(k)": true & k !~ _placeholderRe && v !~ _placeholderRe}
		for k, v in out.labels {"podLabels \(k)": true & k !~ _placeholderRe && v !~ _placeholderRe}
	}
	podAnnotationKey: {
		for k, _ in out.annotations if k !~ _placeholderRe {(k): true & (k & #QualifiedName) != _|_}
	}
	podLabelKey: {
		for k, _ in out.labels if k !~ _placeholderRe {(k): true & (k & #QualifiedName) != _|_}
	}
	podLabelValue: {
		for k, v in out.labels if v !~ _placeholderRe {(k): true & (v & #LabelValue) != _|_}
	}
}

// #PodMetadataConflicts fails when two sources set the same key to
// different values. Identical key and value from several sources is fine:
// several Vault Agent inputs may each repeat agent-inject: "true".
#PodMetadataConflicts: {
	// Expanded metadata (#PodMetadata.out), keyed by source name: a
	// variable, a file input, or "(shared)" for the values document's own.
	sources: [string]: {annotations: [string]: string, labels: [string]: string}

	conflictingPodAnnotation: {
		for a, ma in sources for b, mb in sources if a < b
		for k, va in ma.annotations if mb.annotations[k] != _|_ {
			"\(k)": "\(a) and \(b)": true & va == mb.annotations[k]
		}
	}
	conflictingPodLabel: {
		for a, ma in sources for b, mb in sources if a < b
		for k, va in ma.labels if mb.labels[k] != _|_ {
			"\(k)": "\(a) and \(b)": true & va == mb.labels[k]
		}
	}
}
