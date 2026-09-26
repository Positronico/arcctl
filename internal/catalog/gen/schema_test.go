package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

type schema = map[string]any

func loadSchema(t *testing.T) schema {
	t.Helper()
	data, err := os.ReadFile("../facts/" + schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	var s schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func parseTree(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

type schemaValidator struct {
	root schema
	errs []string
}

var annotationKeywords = []string{"$schema", "$id", "title", "description", "$defs"}

func (v *schemaValidator) resolve(ref string) schema {
	name, ok := strings.CutPrefix(ref, "#/$defs/")
	if !ok {
		panic("unsupported $ref " + ref)
	}
	s, ok := v.root["$defs"].(schema)[name].(schema)
	if !ok {
		panic("unknown $ref " + ref)
	}
	return s
}

func (v *schemaValidator) valid(s schema, x any) bool {
	sub := &schemaValidator{root: v.root}
	sub.check(s, x, "")
	return len(sub.errs) == 0
}

func (v *schemaValidator) fail(path, format string, args ...any) {
	v.errs = append(v.errs, fmt.Sprintf("%s: %s", pathOrRoot(path), fmt.Sprintf(format, args...)))
}

func (v *schemaValidator) check(s schema, x any, path string) {
	for _, kw := range slices.Sorted(maps.Keys(s)) {
		arg := s[kw]
		switch kw {
		case "$ref":
			v.check(v.resolve(arg.(string)), x, path)
		case "type":
			types, ok := arg.([]any)
			if !ok {
				types = []any{arg}
			}
			if !slices.ContainsFunc(types, func(t any) bool { return hasType(x, t.(string)) }) {
				v.fail(path, "type is not %v", arg)
			}
		case "enum":
			if !slices.ContainsFunc(arg.([]any), func(e any) bool { return canonical(e) == canonical(x) }) {
				v.fail(path, "not in enum %v", arg)
			}
		case "const":
			if canonical(arg) != canonical(x) {
				v.fail(path, "not the constant %v", arg)
			}
		case "properties":
			if obj, ok := x.(map[string]any); ok {
				props := arg.(schema)
				for _, k := range slices.Sorted(maps.Keys(props)) {
					if val, ok := obj[k]; ok {
						v.check(props[k].(schema), val, joinPath(path, k))
					}
				}
			}
		case "required":
			if obj, ok := x.(map[string]any); ok {
				for _, k := range arg.([]any) {
					if _, ok := obj[k.(string)]; !ok {
						v.fail(path, "missing required %q", k)
					}
				}
			}
		case "additionalProperties":
			if obj, ok := x.(map[string]any); ok {
				props, _ := s["properties"].(schema)
				for _, k := range slices.Sorted(maps.Keys(obj)) {
					if _, known := props[k]; known {
						continue
					}
					switch a := arg.(type) {
					case bool:
						if !a {
							v.fail(path, "unexpected property %q", k)
						}
					case schema:
						v.check(a, obj[k], joinPath(path, k))
					}
				}
			}
		case "propertyNames":
			if obj, ok := x.(map[string]any); ok {
				for _, k := range slices.Sorted(maps.Keys(obj)) {
					v.check(arg.(schema), k, joinPath(path, k))
				}
			}
		case "items":
			if arr, ok := x.([]any); ok {
				for i, e := range arr {
					v.check(arg.(schema), e, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		case "minItems", "maxItems":
			if arr, ok := x.([]any); ok {
				n := number(arg)
				if kw == "minItems" && float64(len(arr)) < n || kw == "maxItems" && float64(len(arr)) > n {
					v.fail(path, "%s %v, have %d", kw, n, len(arr))
				}
			}
		case "uniqueItems":
			if arr, ok := x.([]any); ok && arg.(bool) {
				seen := map[string]bool{}
				for _, e := range arr {
					c := canonical(e)
					if seen[c] {
						v.fail(path, "duplicate item %s", c)
					}
					seen[c] = true
				}
			}
		case "minLength", "maxLength":
			if str, ok := x.(string); ok {
				n, l := number(arg), float64(utf8.RuneCountInString(str))
				if kw == "minLength" && l < n || kw == "maxLength" && l > n {
					v.fail(path, "%s %v, have %v", kw, n, l)
				}
			}
		case "pattern":
			if str, ok := x.(string); ok && !regexp.MustCompile(arg.(string)).MatchString(str) {
				v.fail(path, "%q does not match %s", str, arg)
			}
		case "minimum", "maximum":
			if num, ok := x.(json.Number); ok {
				n, lim := number(num), number(arg)
				if kw == "minimum" && n < lim || kw == "maximum" && n > lim {
					v.fail(path, "%s %v, have %v", kw, lim, n)
				}
			}
		case "allOf":
			for _, sub := range arg.([]any) {
				v.check(sub.(schema), x, path)
			}
		case "anyOf":
			if !slices.ContainsFunc(arg.([]any), func(sub any) bool { return v.valid(sub.(schema), x) }) {
				v.fail(path, "matches no anyOf branch")
			}
		case "not":
			if v.valid(arg.(schema), x) {
				v.fail(path, "matches a not schema")
			}
		case "if":
			branch := "else"
			if v.valid(arg.(schema), x) {
				branch = "then"
			}
			if sub, ok := s[branch].(schema); ok {
				v.check(sub, x, path)
			}
		case "then", "else":
		default:
			if !slices.Contains(annotationKeywords, kw) {
				panic("unsupported schema keyword " + kw)
			}
		}
	}
}

func hasType(x any, t string) bool {
	switch t {
	case "object":
		_, ok := x.(map[string]any)
		return ok
	case "array":
		_, ok := x.([]any)
		return ok
	case "string":
		_, ok := x.(string)
		return ok
	case "boolean":
		_, ok := x.(bool)
		return ok
	case "null":
		return x == nil
	case "integer":
		n, ok := x.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseInt(n.String(), 10, 64)
		return err == nil
	case "number":
		_, ok := x.(json.Number)
		return ok
	}
	panic("unsupported type " + t)
}

func number(x any) float64 {
	switch n := x.(type) {
	case float64:
		return n
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			panic(err)
		}
		return f
	}
	panic(fmt.Sprintf("not a number: %v", x))
}

func canonical(x any) string {
	if n, ok := x.(float64); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	if n, ok := x.(json.Number); ok {
		return strconv.FormatFloat(number(n), 'f', -1, 64)
	}
	b, err := json.Marshal(x)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func schemaErrors(root schema, instance any) []string {
	v := &schemaValidator{root: root}
	v.check(root, instance, "")
	return v.errs
}

func (v *schemaValidator) flatten(s schema) schema {
	out := schema{}
	for k, val := range s {
		out[k] = val
	}
	if ref, ok := s["$ref"].(string); ok {
		delete(out, "$ref")
		for k, val := range v.flatten(v.resolve(ref)) {
			if _, set := out[k]; !set {
				out[k] = val
			}
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		delete(out, "allOf")
		for _, sub := range all {
			for k, val := range v.flatten(sub.(schema)) {
				if _, set := out[k]; !set {
					out[k] = val
				}
			}
		}
	}
	return out
}

func jsonTypes(s schema) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, e.(string))
		}
		return out
	}
	var vals []any
	if e, ok := s["enum"].([]any); ok {
		vals = e
	} else if c, ok := s["const"]; ok {
		vals = []any{c}
	}
	var out []string
	for _, val := range vals {
		switch val.(type) {
		case string:
			out = append(out, "string")
		case float64:
			out = append(out, "integer")
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (v *schemaValidator) agree(t *testing.T, goType reflect.Type, s schema, path string) {
	s = v.flatten(s)
	types := jsonTypes(s)
	nonNull := slices.DeleteFunc(slices.Clone(types), func(x string) bool { return x == "null" })
	if goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	want := map[reflect.Kind]string{
		reflect.Struct: "object", reflect.Map: "object", reflect.Slice: "array", reflect.Array: "array",
		reflect.String: "string", reflect.Bool: "boolean",
		reflect.Int: "integer", reflect.Uint8: "integer", reflect.Uint16: "integer",
	}[goType.Kind()]
	if len(nonNull) != 1 || nonNull[0] != want {
		t.Errorf("%s: Go %s, schema types %v", pathOrRoot(path), goType, types)
		return
	}
	switch goType.Kind() {
	case reflect.Uint8, reflect.Uint16, reflect.Int:
		limits := map[reflect.Kind]float64{reflect.Uint8: 255, reflect.Uint16: 65535}
		min, hasMin := s["minimum"]
		max, hasMax := s["maximum"]
		unsigned := hasMin && number(min) == 0 && hasMax && (number(max) == 255 || number(max) == 65535)
		switch limit, isUint := limits[goType.Kind()]; {
		case isUint && (!unsigned || number(max) != limit):
			t.Errorf("%s: Go %s needs the schema range 0..%v", path, goType, limit)
		case !isUint && unsigned:
			t.Errorf("%s: schema range 0..%v needs a Go uint8 or uint16, not %s", path, number(max), goType)
		}
	case reflect.Slice, reflect.Array:
		if goType.Kind() == reflect.Array {
			n := float64(goType.Len())
			if min, ok := s["minItems"]; !ok || number(min) != n {
				t.Errorf("%s: Go %s needs schema minItems %v", path, goType, n)
			}
			if max, ok := s["maxItems"]; !ok || number(max) != n {
				t.Errorf("%s: Go %s needs schema maxItems %v", path, goType, n)
			}
		}
		items, ok := s["items"].(schema)
		if !ok {
			t.Errorf("%s: schema array without items", path)
			return
		}
		v.agree(t, goType.Elem(), items, path+"[]")
	case reflect.Map:
		if _, ok := s["properties"]; ok {
			t.Errorf("%s: Go map, schema has fixed properties", path)
		}
		add, ok := s["additionalProperties"].(schema)
		if !ok {
			t.Errorf("%s: Go map, schema has no additionalProperties schema", path)
			return
		}
		v.agree(t, goType.Elem(), add, path+"{}")
	case reflect.Struct:
		props, _ := s["properties"].(schema)
		if s["additionalProperties"] != false {
			t.Errorf("%s: schema object must set additionalProperties false", path)
		}
		var required []string
		reqs, _ := s["required"].([]any)
		for _, r := range reqs {
			required = append(required, r.(string))
		}
		var goNames, goRequired []string
		for i := range goType.NumField() {
			f := goType.Field(i)
			name, optional := jsonTag(f)
			goNames = append(goNames, name)
			p := joinPath(path, name)
			ps, ok := props[name].(schema)
			if !ok {
				continue
			}
			nullable := slices.Contains(jsonTypes(v.flatten(ps)), "null")
			isPtr := f.Type.Kind() == reflect.Pointer
			switch {
			case !optional && isPtr && !nullable:
				t.Errorf("%s: required Go pointer, schema does not allow null", p)
			case nullable && (optional || !isPtr):
				t.Errorf("%s: schema allows null, Go needs a required pointer", p)
			}
			if !optional {
				goRequired = append(goRequired, name)
			}
			v.agree(t, f.Type, ps, p)
		}
		slices.Sort(goNames)
		slices.Sort(goRequired)
		slices.Sort(required)
		if schemaNames := slices.Sorted(maps.Keys(props)); !slices.Equal(goNames, schemaNames) {
			t.Errorf("%s: Go properties %v, schema properties %v", pathOrRoot(path), goNames, schemaNames)
		}
		if !slices.Equal(goRequired, required) {
			t.Errorf("%s: Go required %v, schema required %v", pathOrRoot(path), goRequired, required)
		}
	}
}

func TestTypesAgreeWithSchema(t *testing.T) {
	root := loadSchema(t)
	v := &schemaValidator{root: root}
	v.agree(t, reflect.TypeFor[facts](), root, "")
}

func TestFactsValidateAgainstSchema(t *testing.T) {
	root := loadSchema(t)
	if errs := schemaErrors(root, factsTree(t, readFacts(t))); len(errs) > 0 {
		t.Errorf("committed facts fail schema.json:\n%s", strings.Join(errs, "\n"))
	}
}
