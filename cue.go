package docuconf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// obj is an ordered struct for CUE output. Values are string, bool,
// int64, float64, json.Number, []any or obj.
type obj []kv

type kv struct {
	key string
	val any
}

func (o obj) add(k string, v any) obj { return append(o, kv{k, v}) }

var cueIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// cueKeywords cannot be used as unquoted labels without ambiguity.
var cueKeywords = map[string]bool{
	"package": true, "import": true, "for": true, "in": true, "if": true,
	"let": true, "true": true, "false": true, "null": true, "div": true,
	"mod": true, "quo": true, "rem": true, "func": true,
}

func cueLabel(k string) string {
	if cueIdentRe.MatchString(k) && !cueKeywords[k] {
		return k
	}
	return cueString(k)
}

// cueString writes a CUE string literal. JSON escaping is valid CUE, and
// a backslash is always escaped, so "\(" can never start an interpolation.
func cueString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// writeCUE writes o's fields at the given indent depth, aligning the
// colons of consecutive single-line fields as cue fmt does.
func writeCUE(b *strings.Builder, o obj, depth int) {
	indent := strings.Repeat("\t", depth)
	for i := 0; i < len(o); {
		// A run of fields with scalar values is aligned together.
		j := i
		width := 0
		for j < len(o) && isScalar(o[j].val) {
			if w := len(cueLabel(o[j].key)); w > width {
				width = w
			}
			j++
		}
		if j > i {
			for _, f := range o[i:j] {
				l := cueLabel(f.key)
				fmt.Fprintf(b, "%s%s:%s%s\n", indent, l, strings.Repeat(" ", width-len(l)+1), cueScalar(f.val))
			}
			i = j
			continue
		}
		f := o[i]
		fmt.Fprintf(b, "%s%s: ", indent, cueLabel(f.key))
		writeValue(b, f.val, depth)
		b.WriteByte('\n')
		i++
	}
}

func writeValue(b *strings.Builder, v any, depth int) {
	switch x := v.(type) {
	case obj:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		writeCUE(b, x, depth+1)
		b.WriteString(strings.Repeat("\t", depth) + "}")
	case []any:
		if allScalar(x) {
			b.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(cueScalar(e))
			}
			b.WriteByte(']')
			return
		}
		b.WriteString("[\n")
		for _, e := range x {
			b.WriteString(strings.Repeat("\t", depth+1))
			writeValue(b, e, depth+1)
			b.WriteString(",\n")
		}
		b.WriteString(strings.Repeat("\t", depth) + "]")
	default:
		b.WriteString(cueScalar(v))
	}
}

func isScalar(v any) bool {
	switch v.(type) {
	case obj, []any:
		return false
	}
	return true
}

func allScalar(l []any) bool {
	for _, e := range l {
		if !isScalar(e) {
			return false
		}
	}
	return true
}

func cueScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return cueString(x)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return formatFloat(x)
	case json.Number:
		return x.String()
	}
	panic(fmt.Sprintf("docuconf: unexpected CUE value %T", v))
}

// fromJSON converts a decoded JSON document to ordered data, sorting object
// keys so output is deterministic.
func fromJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var o obj
		for _, k := range keys {
			o = o.add(k, fromJSON(x[k]))
		}
		if o == nil {
			o = obj{}
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fromJSON(e)
		}
		return out
	}
	return v
}
