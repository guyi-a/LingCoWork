package tools

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// jsonschema struct tags are a comma-separated list of key=value pairs, and
// the reflector splits on any comma not preceded by a backslash. A prose
// description containing an unescaped comma is therefore cut at that comma,
// and the rest of the sentence is parsed as bogus keys and dropped — silently,
// with no build or runtime error. The model just receives half a sentence.
//
// This shipped: write_file_chunked's mode reached the model as "Write mode:
// start", so the words append/finish/abort never appeared in the parameter
// docs. The model omitted mode on an append, the chunk was rejected, and the
// finished file was missing that section.
//
// A comma inside a description must be written `\\,` in the Go source. The
// struct tag value is itself a Go-quoted string, so it gets unquoted once by
// reflect before the reflector ever sees it: source `\\,` becomes `\,` at
// that point, which is what the reflector's splitter recognizes. Writing a
// single `\,` in the source is worse than not escaping at all — `\,` is not a
// valid Go escape, unquoting fails, and reflect drops the ENTIRE tag, so the
// field reaches the model with no description and no enum. Both mistakes are
// checked here.
func TestJSONSchemaTagsHaveNoUnescapedCommas(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var checked int

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "data", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil // not our job to police unparseable files
		}
		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, f := range field.Fields.List {
				if f.Tag == nil {
					continue
				}
				raw, err := strconv.Unquote(f.Tag.Value)
				if err != nil {
					continue
				}
				pos := fset.Position(f.Tag.Pos())
				tag, ok := reflect.StructTag(raw).Lookup("jsonschema")
				if !ok {
					// The key is spelled in the source but reflect refuses to
					// hand it over: the value failed to unquote, most likely a
					// lone `\,`. Nothing about this is visible at runtime.
					if strings.Contains(raw, "jsonschema:") {
						t.Errorf("%s:%d: reflect cannot parse this struct tag, so the field reaches "+
							"the model with NO description — a comma must be escaped `\\\\,` not `\\,`\n\tsource: %s",
							filepath.Base(pos.Filename), pos.Line, raw)
					}
					continue
				}
				if tag == "" {
					continue
				}
				checked++
				for _, seg := range splitOnUnescapedCommas(tag) {
					if isKnownJSONSchemaKeyword(seg) {
						continue
					}
					t.Errorf("%s:%d: jsonschema tag splits into an unrecognized segment %q — "+
						"an unescaped comma in a description truncates it. Write commas as \\,\n\tfull tag: %s",
						filepath.Base(pos.Filename), pos.Line, seg, tag)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("scanned no jsonschema tags — the walk is looking in the wrong place")
	}
	t.Logf("checked %d jsonschema tags", checked)
}

// jsonSchemaKeywords is the set this codebase is allowed to use. Adding a new
// upstream keyword means adding it here; that is the point, since the
// alternative is a typo'd key being silently ignored.
var jsonSchemaKeywords = map[string]bool{
	"description": true, "enum": true, "required": true, "type": true,
	"format": true, "pattern": true, "default": true, "example": true,
	"title": true, "minimum": true, "maximum": true, "exclusiveMinimum": true,
	"exclusiveMaximum": true, "multipleOf": true, "minLength": true,
	"maxLength": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"nullable": true, "readOnly": true, "writeOnly": true, "deprecated": true,
	"oneof_ref": true, "oneof_type": true, "anyof_ref": true, "anyof_type": true,
	"additionalProperties": true, "-": true,
}

func isKnownJSONSchemaKeyword(segment string) bool {
	key := segment
	if i := strings.Index(segment, "="); i >= 0 {
		key = segment[:i]
	}
	return jsonSchemaKeywords[key]
}

// splitOnUnescapedCommas mirrors the reflector's own splitting so the test
// sees exactly the segments the schema generator sees.
func splitOnUnescapedCommas(tag string) []string {
	parts := strings.Split(tag, ",")
	out := []string{parts[0]}
	for _, next := range parts[1:] {
		last := len(out) - 1
		if len(out[last]) > 0 && out[last][len(out[last])-1] == '\\' {
			out[last] = out[last][:len(out[last])-1] + "," + next
			continue
		}
		out = append(out, next)
	}
	return out
}

// The specific regression: the model must be able to see every valid mode.
func TestChunkedWriteModeDescriptionListsEveryMode(t *testing.T) {
	tl, err := newChunkedWriteFileTool(&fsDeps{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := tl.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	mode, ok := js.Properties.Get("mode")
	if !ok {
		t.Fatal("mode property missing from schema")
	}
	for _, want := range []string{"start", "append", "finish", "abort"} {
		if !strings.Contains(mode.Description, want) {
			t.Errorf("mode description %q does not mention %q", mode.Description, want)
		}
	}
}
