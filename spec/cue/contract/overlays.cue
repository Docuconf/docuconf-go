package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/toml"
	"encoding/yaml"
	"list"
	"path"
	"strings"
)

// Config-file overlays (SPEC §4.7).
//
// Hosts such as .NET, Spring Boot and Rails layer configuration files under
// environment variables. An overlay is one more file in the host's own
// format, mounted by the platform between the files baked into the image
// and the environment:
//
//	baked-in base file < profile file < platform overlay < environment variables
//
// The app declares the overlay (where it reads it, in which format, and
// whether it reloads it); the platform chooses which variables to supply
// through it. Each value is written at its variable's configKey, in native
// types, and checked exactly like an env value.

#Overlay: close({
	name:         #InputName
	description?: string & =~"^.{5,}$"
	format:       "json" | "yaml" | "toml"
	// Where the app reads the overlay. Its directory is mounted, so it may
	// not hold a file baked into the image (the SDK checks that).
	path: #AbsPath
	// How configKey splits into nested keys: ":" in .NET
	// (Orders:CheckoutTimeout), "." in Spring (orders.checkout-timeout).
	keySeparator: ":" | "."
	// watch: the app reloads the file (reloadOnChange: true in .NET), so the
	// platform updates it in place. restart: a change rolls the pods.
	reload: *"restart" | "watch"
})

// #Nest places a value at a key path: ["Orders", "Timeout"] -> {Orders: Timeout: v}.
// CUE has no recursion, so paths are limited to #MaxKeyDepth parts.
#MaxKeyDepth: 8

#Nest: {
	parts: [...string]
	value: _
	out: {...}
	let p = parts
	let v = value
	if len(p) == 1 {out: (p[0]): v}
	if len(p) == 2 {out: (p[0]): (p[1]): v}
	if len(p) == 3 {out: (p[0]): (p[1]): (p[2]): v}
	if len(p) == 4 {out: (p[0]): (p[1]): (p[2]): (p[3]): v}
	if len(p) == 5 {out: (p[0]): (p[1]): (p[2]): (p[3]): (p[4]): v}
	if len(p) == 6 {out: (p[0]): (p[1]): (p[2]): (p[3]): (p[4]): (p[5]): v}
	if len(p) == 7 {out: (p[0]): (p[1]): (p[2]): (p[3]): (p[4]): (p[5]): (p[6]): v}
	if len(p) == 8 {out: (p[0]): (p[1]): (p[2]): (p[3]): (p[4]): (p[5]): (p[6]): (p[7]): v}
}

// #CheckOverlayValue checks one overlay value against its variable. The
// rules that make a variable eligible for an overlay (not secret, has a
// configKey, a literal) are in #Validate, where the values are concrete.
#CheckOverlayValue: {
	var:      #Var
	value:    _
	#schema?: _

	let Var = var
	let V = value
	check: #Check & {var: Var, value: V}
	if #schema != _|_ {
		let S = #schema
		check: #schema: S
	}
}

// #OverlayData is the overlay's content as data: each value at its
// configKey, in native types (numbers, booleans, lists), with durations
// written in the variable's encoding because the host parses them from text.
#OverlayData: {
	overlay: #Overlay
	vars: [string]: #Var
	values: [string]: _
	out: {
		// Sorted, so the file (and its hash) does not depend on the order the
		// platform wrote its values in.
		// (Embedded structs come out last-first, hence descending.)
		for n in list.Sort([for k, _ in values {k}], list.Descending) {
			let x = values[n]
			let v = vars[n]
			let native = [
				if v.type == "duration" {(#RenderDuration & {in: x, encoding: v.encoding}).out},
				x,
			][0]
			(#Nest & {parts: strings.Split(v.configKey, overlay.keySeparator), value: native}).out
		}
	}
}

// #RenderOverlay produces the ConfigMap, volume and mount for one overlay.
#RenderOverlay: {
	service: string
	overlay: #Overlay
	vars: [string]: #Var
	values: [string]: _

	let O = overlay
	let D = (#OverlayData & {"overlay": O, "vars": vars, "values": values}).out
	let content = [
		if O.format == "json" {json.Indent(json.Marshal(D), "", "  ") + "\n"},
		if O.format == "yaml" {yaml.Marshal(D)},
		toml.Marshal(D),
	][0]
	let fileName = path.Base(O.path, path.Unix)
	let vol = "dc-overlay-\(O.name)"
	let hash = strings.SliceRunes(hex.Encode(sha256.Sum256(content)), 0, 10)

	// watch: a stable name and a mutable ConfigMap, so the kubelet updates
	// the mounted file in place and the app reloads it. restart: content-
	// hashed and immutable, so any change rolls the pods.
	let cm = [if O.reload == "watch" {"\(service)-overlay-\(O.name)"}, "\(service)-overlay-\(O.name)-\(hash)"][0]

	configMap: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		metadata: name: cm
		if O.reload == "restart" {immutable: true}
		data: (fileName): content
	}
	volume: {name: vol, configMap: {name: cm, defaultMode: 292, items: [{key: fileName, path: fileName}]}}
	volumeMount: {name: vol, mountPath: path.Dir(O.path, path.Unix), readOnly: true}
}
