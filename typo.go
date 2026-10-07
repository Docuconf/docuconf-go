package docuconf

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// varTagKeys are the struct tag keys docuconf reads on a variable field.
var varTagKeys = []string{
	"env", "envDefault", "envSeparator", "envPrefix",
	"secret", "desc", "group", "examples", "deprecated", "configKey", "type",
	"minLength", "maxLength", "pattern", "min", "max", "schemes", "values",
	"minItems", "maxItems", "itemMin", "itemMax",
}

// hostTagKeys are tag keys other common libraries own. They are never
// reported as typos of a docuconf key, however close they are.
var hostTagKeys = []string{
	"env", "envDefault", "envPrefix", "envSeparator", "envKeyValSeparator", "envExpand",
	"json", "yaml", "toml", "xml", "hcl", "mapstructure", "koanf", "msgpack", "bson",
	"protobuf", "db", "gorm", "form", "query", "uri", "header", "binding", "validate",
	"default", "required", "split_words", "ignored", "envconfig", "flag", "usage",
	"description", "long", "short", "help", "arg", "name",
}

// tagKeys returns the keys of a struct tag in the conventional
// key:"value" format, in order. It stops at the first malformed entry,
// as reflect.StructTag.Lookup does.
func tagKeys(tag reflect.StructTag) []string {
	var keys []string
	s := string(tag)
	for s != "" {
		i := 0
		for i < len(s) && s[i] == ' ' {
			i++
		}
		s = s[i:]
		if s == "" {
			break
		}
		i = 0
		for i < len(s) && s[i] > ' ' && s[i] != ':' && s[i] != '"' && s[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(s) || s[i] != ':' || s[i+1] != '"' {
			break
		}
		name := s[:i]
		s = s[i+1:]
		i = 1
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(s) {
			break
		}
		if _, err := strconv.Unquote(s[:i+1]); err != nil {
			break
		}
		s = s[i+1:]
		keys = append(keys, name)
	}
	return keys
}

// tagTypos returns one problem for each tag key on a field that is not a
// key docuconf or a common library reads, but is close to one docuconf
// reads: secrte for secret, mni for min. A typo would otherwise drop the
// rule silently, and a typo'd secret would export the field as plain.
func tagTypos(tag reflect.StructTag, known []string) []string {
	var problems []string
	for _, k := range tagKeys(tag) {
		if slices.Contains(known, k) || slices.Contains(hostTagKeys, k) {
			continue
		}
		if hint := closest(k, known, 2); hint != "" {
			problems = append(problems, fmt.Sprintf("unknown tag %q; did you mean %q?", k, hint))
		}
	}
	return problems
}

// closest returns the candidate within maxDist edits of s (ignoring
// case), or "" when there is none. Short candidates allow fewer edits, so
// that unrelated short words do not match.
func closest(s string, candidates []string, maxDist int) string {
	best, bestDist := "", maxDist+1
	ls := strings.ToLower(s)
	for _, c := range candidates {
		limit := maxDist
		if len(c) < 6 && limit > 1 {
			limit = 1
		}
		d := editDistance(ls, strings.ToLower(c))
		if d <= limit && d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// editDistance is the optimal string alignment distance: insertions,
// deletions, substitutions and swaps of adjacent characters each cost 1.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}
