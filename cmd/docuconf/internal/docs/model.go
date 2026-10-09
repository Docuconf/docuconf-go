// Package docs builds the docuconf docs model from a contract and renders
// it (SPEC §14).
//
// The pipeline has three stages, and only the first one reads a contract:
//
//	contract JSON ──Build──▶ *Model ──Encode──▶ docs.json
//	                            │
//	                            ├──Markdown──▶ CONFIG.md         (developers)
//	                            └──Agents────▶ CONFIG.agents.md  (AI agents)
//
// The renderers read nothing but the model, so a docs.json from anywhere
// renders the same way, and third parties can build their own renderers
// from it. Every phrase a renderer shows (constraints, wire formats,
// sources, boot errors) is in the model, so all renderers word them alike.
package docs

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// APIVersion and Kind identify a docs model document.
const (
	APIVersion = "docs.docuconf.dev/v1alpha1"
	Kind       = "ConfigDocs"
)

// Model is the docs model: the schema is #DocsModel in spec/cue/docs.
type Model struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Service    Service     `json:"service"`
	Profiles   *Profiles   `json:"profiles,omitempty"`
	Overlays   []Overlay   `json:"overlays,omitempty"`
	Groups     []Group     `json:"groups"`
	Errors     []ErrorInfo `json:"errors"`
}

// Service identifies the contract the model was built from.
type Service struct {
	Name       string    `json:"name"`
	AppVersion string    `json:"appVersion,omitempty"`
	Generator  Generator `json:"generator"`
}

// Generator is the SDK that exported the contract.
type Generator struct {
	Language string `json:"language"`
	SDK      string `json:"sdk"`
	Version  string `json:"version"`
}

// Profiles describes the profile selector (SPEC §4.4).
type Profiles struct {
	Selector string   `json:"selector"`
	Default  string   `json:"default"`
	Names    []string `json:"names"`
}

// Overlay is a config-file overlay the platform may write (SPEC §4.7).
type Overlay struct {
	Name         string `json:"name"`
	Format       string `json:"format"`
	Path         string `json:"path"`
	KeySeparator string `json:"keySeparator"`
	Reload       string `json:"reload"`
}

// Group holds the inputs of one contract group. Name is "" for inputs
// without a group.
type Group struct {
	Name   string  `json:"name"`
	Title  string  `json:"title"`
	Inputs []Input `json:"inputs"`
}

// Input kinds.
const (
	KindVar  = "var"
	KindFile = "file"
)

// Input is one variable or file input.
type Input struct {
	Name            string           `json:"name"`
	Kind            string           `json:"kind"`
	Type            string           `json:"type"`
	TypeLabel       string           `json:"typeLabel"`
	Group           string           `json:"group,omitempty"`
	Required        bool             `json:"required"`
	Secret          bool             `json:"secret"`
	Description     string           `json:"description"`
	Details         string           `json:"details,omitempty"`
	Deprecated      *Deprecated      `json:"deprecated,omitempty"`
	Default         json.RawMessage  `json:"default,omitempty"`
	DefaultEnv      []EnvEntry       `json:"defaultEnv,omitempty"`
	Examples        []string         `json:"examples,omitempty"`
	ConfigKey       string           `json:"configKey,omitempty"`
	ProfileSelector bool             `json:"profileSelector,omitempty"`
	ProfileDefaults []ProfileDefault `json:"profileDefaults,omitempty"`
	Wire            *Wire            `json:"wire,omitempty"`
	Rotation        *Rotation        `json:"rotation,omitempty"`
	File            *FileInfo        `json:"file,omitempty"`
	Constraints     []Constraint     `json:"constraints"`
	Fields          []Field          `json:"fields,omitempty"`
	Sources         []Source         `json:"sources"`
	Errors          []string         `json:"errors"`
}

// Deprecated is a deprecation notice.
type Deprecated struct {
	Message    string `json:"message"`
	ReplacedBy string `json:"replacedBy,omitempty"`
}

// EnvEntry is one environment variable as the process sees it.
type EnvEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ProfileDefault is a default from one of the app's profile files.
type ProfileDefault struct {
	Profile string          `json:"profile"`
	Value   json.RawMessage `json:"value"`
}

// Wire describes how a variable's value is written.
type Wire struct {
	Encoding  string `json:"encoding,omitempty"`
	Separator string `json:"separator,omitempty"`
	// Text says how the value is written in the process environment.
	Text string `json:"text"`
	// Platform says how it is written in a platform values file.
	Platform string `json:"platform"`
}

// Rotation says how a key set's keys are rotated (SPEC §6.1): Text
// introduces the steps, in order.
type Rotation struct {
	Text  string   `json:"text"`
	Steps []string `json:"steps"`
}

// Field is one row of a JSON Schema's field table (SPEC §14.3): a
// property of a json variable's value or of a config file. Path is
// dotted, with [] for the items of a list. A subtree the table cannot
// express has Type "see schema" and its raw Schema.
type Field struct {
	Path        string            `json:"path"`
	Type        string            `json:"type"`
	Required    bool              `json:"required"`
	Default     json.RawMessage   `json:"default,omitempty"`
	Description string            `json:"description,omitempty"`
	Enum        []json.RawMessage `json:"enum,omitempty"`
	Constraints []Constraint      `json:"constraints"`
	Schema      json.RawMessage   `json:"schema,omitempty"`
}

// FileInfo holds what is particular to a file input.
type FileInfo struct {
	Path       string `json:"path"`
	PathEnv    string `json:"pathEnv,omitempty"`
	Format     string `json:"format,omitempty"`
	Reload     string `json:"reload"`
	MaxSize    int64  `json:"maxSize,omitempty"`
	Contents   string `json:"contents"`
	ReloadText string `json:"reloadText"`
}

// Constraint is one constraint, as data (Params, under the contract's
// field names) and as a phrase (Text, CommonMark inline syntax).
type Constraint struct {
	Rule   string                     `json:"rule"`
	Params map[string]json.RawMessage `json:"params"`
	Text   string                     `json:"text"`
}

// Source is a way the platform may supply an input. Text explains the
// kind and is the same for every input; Note, when present, is what is
// particular to this input.
type Source struct {
	Kind    string `json:"kind"`
	Overlay string `json:"overlay,omitempty"`
	Note    string `json:"note,omitempty"`
	Text    string `json:"text"`
}

// ErrorInfo explains a boot error code.
type ErrorInfo struct {
	Code    string `json:"code"`
	Meaning string `json:"meaning"`
	Fix     string `json:"fix"`
}

// Encode writes the model as indented JSON with a final newline. The
// output depends only on the model: no timestamps, sorted object keys in
// constraint params, and no HTML escaping.
func Encode(m *Model) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode reads a docs model. It checks the apiVersion and kind and rejects
// unknown fields; the full schema check is #DocsModel, which the CLI runs
// before it decodes.
func Decode(data []byte) (*Model, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Model
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("docs model: %w", err)
	}
	if m.APIVersion != APIVersion || m.Kind != Kind {
		return nil, fmt.Errorf("docs model: want apiVersion %s and kind %s, got %q and %q", APIVersion, Kind, m.APIVersion, m.Kind)
	}
	return &m, nil
}

// IsModel reports whether a JSON document is a docs model rather than a
// contract, from its apiVersion.
func IsModel(data []byte) bool {
	var head struct {
		APIVersion string `json:"apiVersion"`
	}
	return json.Unmarshal(data, &head) == nil && head.APIVersion == APIVersion
}
