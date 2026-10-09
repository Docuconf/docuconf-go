package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

const diffUsage = `Usage: docuconf diff <old.cue | old.json | -> <new.cue | new.json | -> [--format text|json] [--allow-breaking] [--ack file]

Classifies every change between two contracts by SPEC §9: compatible (ok),
notable, or breaking. A breaking change is either breaking for everyone, or
breaking for the platform only (values that still set a removed input).
One side may be "-" to read that contract from standard input.

Exit status: 0 when no change is breaking (or every breaking change is
acknowledged, or --allow-breaking is set), 1 when a change is breaking, 2
on a usage or parse error.

--ack names a file of accepted changes, one per line: the input name and
the change id, as printed in brackets (the "change" field of --format json). Lines starting with # are comments.

  # the platform stopped setting it in values PR #142
  LEGACY_MODE var-removed

`

// errBreaking means diff found an unacknowledged breaking change; the
// changes have been printed already.
var errBreaking = errors.New("breaking changes found")

// stdin is where "-" reads from; tests replace it.
var stdin io.Reader = os.Stdin

// class is how a change affects a deploy, from SPEC §9.
type class int

const (
	compatible class = iota
	notable
	breakingPlatform // breaking for the platform only
	breaking
)

func (c class) String() string {
	return [...]string{"compatible", "notable", "breaking-platform", "breaking"}[c]
}

func (c class) label() string {
	return [...]string{"ok", "NOTABLE", "BREAKING", "BREAKING"}[c]
}

func (c class) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// change is one classified difference between two contracts.
type change struct {
	Input        string `json:"input"`
	Change       string `json:"change"`
	Class        class  `json:"class"`
	Reason       string `json:"reason"`
	Acknowledged bool   `json:"acknowledged,omitempty"`

	section int // for sorting: metadata, vars, files, overlays, profiles
	seq     int
}

const (
	secMeta = iota
	secVars
	secFiles
	secOverlays
	secProfiles
)

type obj = map[string]any

func runDiff(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, diffUsage)
		fs.PrintDefaults()
	}
	format := fs.String("format", "text", "output format: text or json")
	allow := fs.Bool("allow-breaking", false, "exit 0 even when a change is breaking (the changes are still printed)")
	ackFile := fs.String("ack", "", "file of acknowledged changes: <input> <change-id> per line")
	// Flags may come before or after the contracts; "-" is a contract.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != 2 {
		fs.Usage()
		return fmt.Errorf("want two contracts, old and new; got %d", len(positional))
	}
	if positional[0] == "-" && positional[1] == "-" {
		return errors.New("only one side can be read from standard input")
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown --format %q: want text or json", *format)
	}
	var acks map[[2]string]bool
	if *ackFile != "" {
		var err error
		if acks, err = readAcks(*ackFile); err != nil {
			return err
		}
	}

	p, err := platform.New()
	if err != nil {
		return err
	}
	oldC, oldName, err := loadForDiff(p, positional[0])
	if err != nil {
		return err
	}
	newC, newName, err := loadForDiff(p, positional[1])
	if err != nil {
		return err
	}

	changes := diffContracts(oldC, newC)
	used := map[[2]string]bool{}
	failed := false
	counts := map[class]int{}
	for i := range changes {
		c := &changes[i]
		key := [2]string{c.Input, c.Change}
		if c.Class >= breakingPlatform && acks[key] {
			c.Acknowledged = true
			used[key] = true
		}
		if c.Class >= breakingPlatform && !c.Acknowledged {
			failed = true
		}
		counts[c.Class]++
	}
	for k := range acks {
		if !used[k] {
			fmt.Fprintf(stderr, "docuconf diff: %s: acknowledged %s %s matches no breaking change\n", *ackFile, k[0], k[1])
		}
	}

	if *format == "json" {
		if changes == nil {
			changes = []change{}
		}
		data, err := json.MarshalIndent(changes, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\n", data)
	} else {
		for _, c := range changes {
			id := c.Change
			if c.Class == breakingPlatform {
				id += ", platform only"
			}
			if c.Acknowledged {
				id += ", acknowledged"
			}
			fmt.Fprintf(stdout, "%-9s %s: %s [%s]\n", c.Class.label(), c.Input, c.Reason, id)
		}
		name := newName
		if name == "" {
			name = oldName
		}
		if len(changes) == 0 {
			fmt.Fprintf(stdout, "%s: no changes\n", name)
		} else {
			fmt.Fprintf(stdout, "%s: %d breaking, %d notable, %d compatible\n", name,
				counts[breaking]+counts[breakingPlatform], counts[notable], counts[compatible])
		}
	}
	if failed && !*allow {
		return errBreaking
	}
	return nil
}

// loadForDiff parses a contract, unified with the meta-schema so that
// defaults are explicit, and returns it as plain JSON data.
func loadForDiff(p *platform.Platform, file string) (obj, string, error) {
	var src []byte
	var err error
	name := file
	if file == "-" {
		if src, err = io.ReadAll(stdin); err != nil {
			return nil, "", err
		}
		// JSON is recognised by its first character; anything else is CUE.
		name = "stdin.cue"
		if t := bytes.TrimSpace(src); len(t) > 0 && t[0] == '{' {
			name = "stdin.json"
		}
	} else if src, err = os.ReadFile(file); err != nil {
		return nil, "", err
	}
	c, err := p.ParseContract(name, src)
	if err != nil {
		return nil, "", err
	}
	data, err := p.ContractJSON(c)
	if err != nil {
		return nil, "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var o obj
	if err := dec.Decode(&o); err != nil {
		return nil, "", err
	}
	return o, c.Name, nil
}

func readAcks(file string) (map[[2]string]bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	acks := map[[2]string]bool{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want <input> <change-id>, got %q", file, n, line)
		}
		acks[[2]string{fields[0], fields[1]}] = true
	}
	return acks, sc.Err()
}

// differ collects changes.
type differ struct {
	changes []change
	section int
}

func (d *differ) add(input, id string, c class, format string, args ...any) {
	d.changes = append(d.changes, change{
		Input: input, Change: id, Class: c, Reason: fmt.Sprintf(format, args...),
		section: d.section, seq: len(d.changes),
	})
}

// diffContracts classifies every change from old to new.
func diffContracts(old, new obj) []change {
	d := &differ{}

	d.section = secMeta
	for _, k := range []string{"apiVersion", "kind"} {
		if !reflect.DeepEqual(old[k], new[k]) {
			d.add(k, k+"-changed", notable, "%s changed from %s to %s", k, show(old[k]), show(new[k]))
		}
	}
	om, nm := asObj(old["metadata"]), asObj(new["metadata"])
	if !reflect.DeepEqual(om["name"], nm["name"]) {
		d.add("metadata.name", "name-changed", notable,
			"service name changed from %s to %s; the platform may treat it as a different service", show(om["name"]), show(nm["name"]))
	}
	// appVersion and generator change with every build; they are not
	// part of what the app accepts.
	known := map[string]bool{"apiVersion": true, "kind": true, "metadata": true, "vars": true, "files": true, "overlays": true, "profiles": true}
	for _, k := range unionKeys(old, new) {
		if !known[k] && !reflect.DeepEqual(old[k], new[k]) {
			d.add(k, "unclassified", breaking, "%s changed in a way diff cannot classify", k)
		}
	}

	d.section = secVars
	d.vars(asObj(old["vars"]), asObj(new["vars"]), new)
	d.section = secFiles
	d.files(asObj(old["files"]), asObj(new["files"]))
	d.section = secOverlays
	d.overlays(asObj(old["overlays"]), asObj(new["overlays"]))
	d.section = secProfiles
	d.profiles(asObj(old["profiles"]), asObj(new["profiles"]), asObj(new["vars"]))

	sort.SliceStable(d.changes, func(i, j int) bool {
		a, b := d.changes[i], d.changes[j]
		if a.section != b.section {
			return a.section < b.section
		}
		if a.Input != b.Input {
			return a.Input < b.Input
		}
		return a.seq < b.seq
	})
	return d.changes
}

// removed reports an input that is gone. It is breaking for the platform:
// values that still set it fail the unknown-input check (SPEC §7).
func (d *differ) removed(name, id, what string, o obj) {
	if dep := asObj(o["deprecated"]); dep != nil {
		d.add(name, id, breakingPlatform,
			"%s removed; it was deprecated (%s), so a platform that heeded the warning is unaffected; values or sources that still set it fail", what, str(dep["message"]))
		return
	}
	d.add(name, id, breakingPlatform,
		"%s removed without being deprecated first; values or sources that still set it fail the unknown-input check", what)
}

// common compares the fields every variable and file input has.
func (d *differ) common(name string, o, n obj) {
	switch ob, nb := isTrue(o["required"]), isTrue(n["required"]); {
	case !ob && nb:
		d.add(name, "made-required", breaking, "optional → required: existing values or sources that leave it unset no longer validate")
	case ob && !nb:
		d.add(name, "made-optional", compatible, "required → optional")
	}
	od, nd := asObj(o["deprecated"]), asObj(n["deprecated"])
	switch {
	case od == nil && nd != nil:
		msg := str(nd["message"])
		if r := str(nd["replacedBy"]); r != "" {
			msg += "; replaced by " + r
		}
		d.add(name, "deprecated", notable, "deprecated: %s; the platform should stop setting it", msg)
	case od != nil && nd == nil:
		d.add(name, "undeprecated", compatible, "no longer deprecated")
	case !reflect.DeepEqual(od, nd):
		d.add(name, "deprecation-changed", compatible, "deprecation notice changed")
	}
	for _, f := range []string{"description", "details", "group", "examples"} {
		if !reflect.DeepEqual(o[f], n[f]) {
			d.add(name, f+"-changed", compatible, "%s changed (docs only)", f)
		}
	}
}

// varFields are the variable fields diff knows how to classify. A change
// to any other field is reported as breaking, to be safe.
var varFields = map[string]bool{
	"name": true, "type": true, "description": true, "details": true, "required": true, "secret": true,
	"group": true, "examples": true, "configKey": true, "deprecated": true, "default": true,
	"minLength": true, "maxLength": true, "pattern": true, "min": true, "max": true, "encoding": true,
	"schemes": true, "values": true, "items": true, "separator": true, "minItems": true, "maxItems": true,
	"itemMin": true, "itemMax": true, "itemMinLength": true, "itemMaxLength": true,
	"minKeys": true, "maxKeys": true, "keyMinLength": true, "keyMaxLength": true, "schema": true,
}

func (d *differ) vars(old, new obj, newContract obj) {
	hasOverlays := len(asObj(newContract["overlays"])) > 0
	for _, name := range unionKeys(old, new) {
		o, n := asObj(old[name]), asObj(new[name])
		switch {
		case o == nil && isTrue(n["required"]):
			d.add(name, "var-added-required", breaking, "required variable added: existing values do not set it")
			continue
		case o == nil:
			d.add(name, "var-added", compatible, "optional variable added")
			continue
		case n == nil:
			d.removed(name, "var-removed", "variable", o)
			continue
		}
		d.common(name, o, n)

		ot, nt := str(o["type"]), str(n["type"])
		os, ns := isTrue(o["secret"]), isTrue(n["secret"])
		switch {
		case ot == "list" && nt == "keySet" && str(o["items"]) == "string" && os:
			wire := "the wire format is identical, so the Secret that holds the keys and the values document need no change"
			if str(o["encoding"]) != str(n["encoding"]) || str(o["separator"]) != str(n["separator"]) {
				wire = "the encoding changed too, so the Secret's content must be rewritten"
			}
			d.add(name, "list-to-keySet", breaking, "secret list of strings → keySet: the contract and the SDK declaration change; %s", wire)
		case ot != nt:
			d.add(name, "type-changed", breaking, "type changed from %s to %s: the value's shape changes", show(o["type"]), show(n["type"]))
		}
		switch {
		case !os && ns:
			d.add(name, "made-secret", breaking, "now secret: the value must come from a secret reference, so a literal no longer validates")
		case os && !ns:
			d.add(name, "made-non-secret", breaking, "no longer secret: a secretKeyRef is no longer accepted")
		}
		if !reflect.DeepEqual(o["default"], n["default"]) {
			switch {
			case o["default"] == nil:
				d.add(name, "default-added", notable, "default added: %s; unset values now get it", show(n["default"]))
			case n["default"] == nil:
				d.add(name, "default-removed", notable, "default %s removed; unset values are now absent", show(o["default"]))
			default:
				d.add(name, "default-changed", notable, "default changed from %s to %s", show(o["default"]), show(n["default"]))
			}
		}
		if !reflect.DeepEqual(o["configKey"], n["configKey"]) {
			if n["configKey"] == nil && hasOverlays {
				d.add(name, "configKey-removed", breakingPlatform, "configKey removed: an overlay can no longer carry it")
			} else {
				d.add(name, "configKey-changed", notable, "configKey changed from %s to %s", show(o["configKey"]), show(n["configKey"]))
			}
		}
		if ot != nt {
			continue // the constraints of different types do not compare
		}

		// Constraints. Defaults the meta-schema fills in (encoding,
		// separator, minKeys, maxKeys) are explicit on both sides.
		durations := nt == "duration"
		for _, f := range []string{"min", "itemMin", "minLength", "itemMinLength", "minItems", "minKeys", "keyMinLength"} {
			d.bound(name, f, true, durations && f == "min", o, n)
		}
		for _, f := range []string{"max", "itemMax", "maxLength", "itemMaxLength", "maxItems", "maxKeys", "keyMaxLength"} {
			d.bound(name, f, false, durations && f == "max", o, n)
		}
		d.pattern(name, o, n)
		d.set(name, "values", "enum values", o, n, false)
		d.set(name, "schemes", "schemes", o, n, true)
		if !reflect.DeepEqual(o["items"], n["items"]) {
			d.add(name, "items-changed", breaking, "list items changed from %s to %s", show(o["items"]), show(n["items"]))
		}
		if !reflect.DeepEqual(o["encoding"], n["encoding"]) {
			if str(n["encoding"]) == "indexed" {
				d.add(name, "encoding-changed", breakingPlatform,
					"encoding changed from %s to %s: an injected reference can no longer supply it (SPEC §4.5)", show(o["encoding"]), show(n["encoding"]))
			} else {
				d.add(name, "encoding-changed", notable,
					"encoding changed from %s to %s: breaking for the app image only; the platform re-renders the wire value", show(o["encoding"]), show(n["encoding"]))
			}
		}
		if !reflect.DeepEqual(o["separator"], n["separator"]) && reflect.DeepEqual(o["encoding"], n["encoding"]) {
			d.add(name, "separator-changed", notable,
				"separator changed from %s to %s: breaking for the app image only; the platform re-renders the wire value, but an injected value must use the new one", show(o["separator"]), show(n["separator"]))
		}
		d.schema(name, o, n)
		for _, f := range unionKeys(o, n) {
			if !varFields[f] && !reflect.DeepEqual(o[f], n[f]) {
				d.add(name, "unclassified", breaking, "%s changed in a way diff cannot classify", f)
			}
		}
	}
}

// fileFields are the file input fields diff knows how to classify.
var fileFields = map[string]bool{
	"name": true, "type": true, "description": true, "details": true, "required": true, "secret": true,
	"group": true, "deprecated": true, "path": true, "pathEnv": true, "reload": true, "maxSize": true,
	"format": true, "schema": true, "dnsNames": true, "keyAlgorithms": true, "minRemaining": true,
	"requireCA": true, "minCertificates": true, "passwordVar": true, "pattern": true,
	"minLength": true, "maxLength": true,
}

func (d *differ) files(old, new obj) {
	for _, name := range unionKeys(old, new) {
		o, n := asObj(old[name]), asObj(new[name])
		switch {
		case o == nil && isTrue(n["required"]):
			d.add(name, "file-added-required", breaking, "required file input added: existing sources do not supply it")
			continue
		case o == nil:
			d.add(name, "file-added", compatible, "optional file input added")
			continue
		case n == nil:
			d.removed(name, "file-removed", "file input", o)
			continue
		}
		d.common(name, o, n)
		imageOnly := "breaking for the app image only; the platform re-renders the mount and nothing in the values changes"
		for _, f := range []string{"type", "path", "format"} {
			if !reflect.DeepEqual(o[f], n[f]) {
				d.add(name, f+"-changed", notable, "%s changed from %s to %s: %s", f, show(o[f]), show(n[f]), imageOnly)
			}
		}
		if !reflect.DeepEqual(o["pathEnv"], n["pathEnv"]) {
			d.add(name, "pathEnv-changed", notable, "pathEnv changed from %s to %s: %s", show(o["pathEnv"]), show(n["pathEnv"]), imageOnly)
		}
		switch os, ns := isTrue(o["secret"]), isTrue(n["secret"]); {
		case !os && ns:
			d.add(name, "made-secret", breaking, "now secret: inline and configMap sources no longer validate")
		case os && !ns:
			d.add(name, "made-non-secret", compatible, "no longer secret: every source is allowed")
		}
		d.reload(name, o, n)
		if !reflect.DeepEqual(o["passwordVar"], n["passwordVar"]) {
			d.add(name, "passwordVar-changed", notable, "passwordVar changed from %s to %s", show(o["passwordVar"]), show(n["passwordVar"]))
		}
		if str(o["type"]) != str(n["type"]) {
			continue
		}
		for _, f := range []string{"minLength", "minCertificates"} {
			d.bound(name, f, true, false, o, n)
		}
		d.bound(name, "minRemaining", true, true, o, n)
		for _, f := range []string{"maxLength", "maxSize"} {
			d.bound(name, f, false, false, o, n)
		}
		d.pattern(name, o, n)
		d.dnsNames(name, o, n)
		d.set(name, "keyAlgorithms", "keyAlgorithms", o, n, true)
		switch ob, nb := isTrue(o["requireCA"]), isTrue(n["requireCA"]); {
		case !ob && nb:
			d.add(name, "requireCA-tightened", breaking, "requireCA set: the source must now carry ca.crt")
		case ob && !nb:
			d.add(name, "requireCA-loosened", compatible, "requireCA unset")
		}
		d.schema(name, o, n)
		for _, f := range unionKeys(o, n) {
			if !fileFields[f] && !reflect.DeepEqual(o[f], n[f]) {
				d.add(name, "unclassified", breaking, "%s changed in a way diff cannot classify", f)
			}
		}
	}
}

func (d *differ) reload(name string, o, n obj) {
	switch or, nr := str(o["reload"]), str(n["reload"]); {
	case or == nr:
	case nr == "watch":
		d.add(name, "reload-changed", compatible, "reload: %s → watch: the app reloads it itself", or)
	default:
		d.add(name, "reload-changed", notable, "reload: %s → %s: the platform must now roll the pods when the source changes", or, nr)
	}
}

func (d *differ) overlays(old, new obj) {
	for _, name := range unionKeys(old, new) {
		o, n := asObj(old[name]), asObj(new[name])
		input := "overlays." + name
		switch {
		case o == nil:
			d.add(input, "overlay-added", compatible, "overlay added: the platform may supply values through it")
			continue
		case n == nil:
			d.add(input, "overlay-removed", breakingPlatform, "overlay removed: values supplied through it are rejected")
			continue
		}
		for _, f := range []string{"path", "format", "keySeparator"} {
			if !reflect.DeepEqual(o[f], n[f]) {
				d.add(input, f+"-changed", notable,
					"%s changed from %s to %s: breaking for the app image only; the platform re-renders the overlay", f, show(o[f]), show(n[f]))
			}
		}
		d.reload(input, o, n)
		if !reflect.DeepEqual(o["description"], n["description"]) {
			d.add(input, "description-changed", compatible, "description changed (docs only)")
		}
		for _, f := range unionKeys(o, n) {
			switch f {
			case "name", "path", "format", "keySeparator", "reload", "description":
			default:
				if !reflect.DeepEqual(o[f], n[f]) {
					d.add(input, "unclassified", breaking, "%s changed in a way diff cannot classify", f)
				}
			}
		}
	}
}

// profiles compares config files baked into the image (SPEC §4.4). Their
// values are defaults selected by a profile, so changes are notable,
// except removing a value a required variable relied on.
func (d *differ) profiles(old, new, newVars obj) {
	if reflect.DeepEqual(old, new) {
		return
	}
	if !reflect.DeepEqual(old["selector"], new["selector"]) {
		d.add("profiles", "selector-changed", notable, "profile selector changed from %s to %s", show(old["selector"]), show(new["selector"]))
	}
	if !reflect.DeepEqual(old["default"], new["default"]) {
		d.add("profiles", "default-profile-changed", notable,
			"default profile changed from %s to %s: an unset selector now picks different baked-in values", show(old["default"]), show(new["default"]))
	}
	od, nd := asObj(old["defaults"]), asObj(new["defaults"])
	for _, prof := range unionKeys(od, nd) {
		op, np := asObj(od[prof]), asObj(nd[prof])
		for _, v := range unionKeys(op, np) {
			ov, okO := op[v]
			nv, okN := np[v]
			switch {
			case !okO:
				d.add(v, "profile-default-added", notable, "profile %s now sets it to %s", prof, show(nv))
			case !okN && isTrue(asObj(newVars[v])["required"]):
				d.add(v, "profile-default-removed", breaking,
					"profile %s no longer sets this required variable: the platform must now supply it when %s is selected", prof, prof)
			case !okN:
				d.add(v, "profile-default-removed", notable, "profile %s no longer sets it (was %s)", prof, show(ov))
			case !reflect.DeepEqual(ov, nv):
				d.add(v, "profile-default-changed", notable, "profile %s value changed from %s to %s", prof, show(ov), show(nv))
			}
		}
	}
	for _, f := range unionKeys(old, new) {
		switch f {
		case "selector", "default", "defaults":
		default:
			if !reflect.DeepEqual(old[f], new[f]) {
				d.add("profiles", "unclassified", breaking, "profiles.%s changed in a way diff cannot classify", f)
			}
		}
	}
}

// bound compares a lower (min) or upper (max) bound. Adding a bound or
// moving it inward tightens; removing it or moving it outward loosens.
func (d *differ) bound(name, field string, lower, duration bool, o, n obj) {
	ov, okO := o[field]
	nv, okN := n[field]
	if !okO && !okN || reflect.DeepEqual(ov, nv) {
		return
	}
	switch {
	case !okO:
		d.add(name, field+"-tightened", breaking, "%s %s added", field, show(nv))
		return
	case !okN:
		d.add(name, field+"-loosened", compatible, "%s %s removed", field, show(ov))
		return
	}
	c, ok := compare(ov, nv, duration)
	if !ok {
		d.add(name, field+"-changed", breaking, "%s changed from %s to %s, which diff cannot compare", field, show(ov), show(nv))
		return
	}
	if c == 0 {
		return
	}
	dir := "raised"
	if c > 0 {
		dir = "lowered"
	}
	if (c < 0) == lower {
		d.add(name, field+"-tightened", breaking, "%s %s from %s to %s", field, dir, show(ov), show(nv))
	} else {
		d.add(name, field+"-loosened", compatible, "%s %s from %s to %s", field, dir, show(ov), show(nv))
	}
}

// pattern: a new or changed pattern is breaking, since diff cannot tell
// whether one regular expression accepts everything another does.
func (d *differ) pattern(name string, o, n obj) {
	op, okO := o["pattern"]
	np, okN := n["pattern"]
	switch {
	case reflect.DeepEqual(op, np):
	case !okO:
		d.add(name, "pattern-added", breaking, "pattern %s added", show(np))
	case !okN:
		d.add(name, "pattern-removed", compatible, "pattern %s removed", show(op))
	default:
		d.add(name, "pattern-changed", breaking, "pattern changed from %s to %s", show(op), show(np))
	}
}

// set compares a list of allowed values: removing one tightens, adding one
// loosens. When absentMeansAny, a missing list allows anything (schemes,
// keyAlgorithms), so adding the list tightens and removing it loosens.
func (d *differ) set(name, field, what string, o, n obj, absentMeansAny bool) {
	ov, okO := o[field]
	nv, okN := n[field]
	if reflect.DeepEqual(ov, nv) {
		return
	}
	tight, loose := field+"-tightened", field+"-loosened"
	if absentMeansAny && (!okO || !okN) {
		if !okO {
			d.add(name, tight, breaking, "%s now restricted to %s", what, show(nv))
		} else {
			d.add(name, loose, compatible, "%s no longer restricted", what)
		}
		return
	}
	added, removed := setDiff(asList(ov), asList(nv))
	if len(removed) > 0 {
		d.add(name, tight, breaking, "%s removed: %s", what, strings.Join(removed, ", "))
	}
	if len(added) > 0 {
		d.add(name, loose, compatible, "%s added: %s", what, strings.Join(added, ", "))
	}
}

// dnsNames lists names the certificate must cover, so more is tighter.
func (d *differ) dnsNames(name string, o, n obj) {
	ov, nv := asList(o["dnsNames"]), asList(n["dnsNames"])
	added, removed := setDiff(ov, nv)
	if len(added) > 0 {
		d.add(name, "dnsNames-tightened", breaking, "dnsNames added: %s; the existing certificate may not cover them", strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		d.add(name, "dnsNames-loosened", compatible, "dnsNames removed: %s", strings.Join(removed, ", "))
	}
}

// setDiff returns the items only in n (added) and only in o (removed), as
// display strings, ignoring order.
func setDiff(o, n []any) (added, removed []string) {
	in := func(l []any, x any) bool {
		return slices.ContainsFunc(l, func(y any) bool { return reflect.DeepEqual(x, y) })
	}
	for _, x := range n {
		if !in(o, x) {
			added = append(added, show(x))
		}
	}
	for _, x := range o {
		if !in(n, x) {
			removed = append(removed, show(x))
		}
	}
	return added, removed
}

// compare returns -1, 0 or 1 as a is less than, equal to or greater than
// b: numbers exactly, and Go-syntax durations when duration is set.
func compare(a, b any, duration bool) (int, bool) {
	if duration {
		da, err1 := time.ParseDuration(str(a))
		db, err2 := time.ParseDuration(str(b))
		if err1 != nil || err2 != nil {
			return 0, false
		}
		return cmpInt(int64(da), int64(db)), true
	}
	ra, ok1 := rat(a)
	rb, ok2 := rat(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return ra.Cmp(rb), true
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func rat(v any) (*big.Rat, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	return new(big.Rat).SetString(string(n))
}

func asObj(v any) obj {
	o, _ := v.(obj)
	return o
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func isTrue(v any) bool {
	b, _ := v.(bool)
	return b
}

// show formats a value for a message: JSON, or "none" when absent.
func show(v any) string {
	if v == nil {
		return "none"
	}
	if n, ok := v.(json.Number); ok {
		return string(n)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func unionKeys(a, b obj) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range []obj{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}
