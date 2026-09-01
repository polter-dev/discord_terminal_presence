package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// emDashRune is U+2014. The owner's standing rule is that it appears in no
// user-facing copy termp prints (issue #585).
const emDashRune = rune(0x2014)

// TestNoEmDashInUserFacingStringLiterals fails if any string literal in
// cmd/termp contains an em dash.
//
// It parses with go/ast and walks *ast.BasicLit rather than grepping lines,
// which is the whole point: cmd/ and internal/ hold roughly fifty em dashes in
// Go comments, and those are deliberately left alone. A line-based check would
// be swamped by them and would either be permanently red or get muted. An AST
// walk sees string literals only, so comments cannot trip it and a regression in
// printed copy cannot hide behind one.
//
// String literals are a superset of printed copy: a literal that never reaches
// a user still fails here. That is intentional. Deciding which literals are
// user-facing needs dataflow this test has no way to do, and the cost of the
// over-approximation is that a non-printed literal has to spell the character
// some other way, which is not a real cost.
//
// Test files are excluded because their diagnostics are not CLI copy. Tests
// that pin production output are updated alongside the production literal.
func TestNoEmDashInUserFacingStringLiterals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/termp: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				// A raw string with an embedded backtick cannot occur, and any
				// other unquote failure means the literal is not something this
				// guard can read; fall back to the raw source text.
				value = lit.Value
			}
			if strings.ContainsRune(value, emDashRune) {
				t.Errorf("%s: string literal contains an em dash (U+2014): %s\nUser-facing copy must not use it; restructure the sentence into two, and use %q for a not-applicable table cell.",
					fset.Position(lit.Pos()), lit.Value, naPlaceholder)
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("guard inspected no files; it would pass vacuously")
	}
}
