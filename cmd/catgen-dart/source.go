package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file is the go/ast half of the hybrid. reflect knows the field set, the
// JSON tags and the Go kinds; it does not know the doc comments (the best
// documentation in this repo, and the whole reason a Dart client is readable at
// all) and it cannot see an unexported const block at all — internal/
// orchestration's ratatui attribute bits are unexported, so the alternative to
// parsing them is retyping six bit patterns by hand and hoping.
//
// Parsing is deliberately shallow: no type checking, no import resolution. The
// only expressions that must evaluate are the literals in the const blocks the
// emitter names, and a mis-parse there fails loudly (constLit returns an error)
// rather than silently emitting a wrong bitmask.

// source is everything harvested from the Go sources, keyed by "<pkgpath>.<Name>".
//
// groups additionally preserves each const block's membership IN DECLARATION
// ORDER, keyed by its first member. That ordering is load-bearing twice over:
// the emitter walks a group to write a Dart constant class, and the message
// roots are discovered by walking wire's Type block and probing the
// real decoder with each value — so a message type added to the block and wired
// into DecodeDown reaches Dart without anyone editing this generator.
type source struct {
	types  map[string]*typeDoc
	consts map[string]*constDecl
	groups map[string][]string
}

// typeDoc is one struct's documentation: its own doc comment plus a per-field
// one. Fields are keyed by Go field name (what reflect reports), not by JSON
// tag, because that is the key the reflection walk already holds.
type typeDoc struct {
	doc    string
	fields map[string]string
}

// constDecl is one resolved constant: its doc comment, its value already
// rendered as a Dart literal, and the Go literal it came from when that form
// carries information Dart cannot express (Go has binary literals; Dart does
// not, so 0b0000_0100_0000 survives only as a comment).
type constDecl struct {
	doc      string
	dart     string
	goSource string
}

// loadSource parses every listed package directory (repo-root relative) and
// indexes it under its import path. Test files are skipped: they hold neither
// wire types nor the vocabulary, and a broken one should not be able to fail
// code generation.
//
// Files are read in name order rather than through parser.ParseDir. Const
// resolution is order-dependent (one const may name an earlier one), so an
// unordered walk would make the generator non-deterministic — the one property
// a golden-diff drift gate cannot tolerate.
func loadSource(root, modPath string, pkgDirs []string) (*source, error) {
	s := &source{
		types:  map[string]*typeDoc{},
		consts: map[string]*constDecl{},
		groups: map[string][]string{},
	}
	fset := token.NewFileSet()
	for _, rel := range pkgDirs {
		pkgPath := modPath + "/" + filepath.ToSlash(rel)
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("parse %s/%s: %w", rel, n, err)
			}
			if err := s.collectFile(pkgPath, f); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", rel, n, err)
			}
		}
	}
	return s, nil
}

func (s *source) collectFile(pkgPath string, f *ast.File) error {
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gen.Tok {
		case token.TYPE:
			s.collectTypes(pkgPath, gen)
		case token.CONST:
			if err := s.collectConsts(pkgPath, gen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *source) collectTypes(pkgPath string, gen *ast.GenDecl) {
	for _, spec := range gen.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		// A lone `type X struct{...}` carries its doc on the GenDecl; a member of
		// a `type ( ... )` block carries it on the TypeSpec.
		doc := docText(ts.Doc)
		if doc == "" && len(gen.Specs) == 1 {
			doc = docText(gen.Doc)
		}
		td := &typeDoc{doc: doc, fields: map[string]string{}}
		if st, ok := ts.Type.(*ast.StructType); ok && st.Fields != nil {
			for _, fld := range st.Fields.List {
				// Leading doc wins over the trailing line comment; a field with
				// both (Init.Viewer) is documented by the block above it.
				text := docText(fld.Doc)
				if text == "" {
					text = docText(fld.Comment)
				}
				if text == "" {
					continue
				}
				for _, n := range fld.Names {
					td.fields[n.Name] = text
				}
				if len(fld.Names) == 0 { // embedded: name is the type's
					td.fields[embeddedName(fld.Type)] = text
				}
			}
		}
		s.types[pkgPath+"."+ts.Name.Name] = td
	}
}

// collectConsts resolves one const block. Values are evaluated left to right so
// a const naming an earlier one (or an iota-free repetition of the previous
// expression) resolves against what is already in the table.
func (s *source) collectConsts(pkgPath string, gen *ast.GenDecl) error {
	var lastValues []ast.Expr
	var group []string
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		values := vs.Values
		if len(values) == 0 {
			values = lastValues // the `A = 1 \n B` continuation form
		} else {
			lastValues = values
		}
		doc := docText(vs.Doc)
		if doc == "" && len(gen.Specs) == 1 {
			doc = docText(gen.Doc)
		}
		if doc == "" {
			doc = docText(vs.Comment)
		}
		for i, name := range vs.Names {
			if i >= len(values) {
				break
			}
			dart, goSrc, err := s.constLit(pkgPath, values[i])
			if err != nil {
				// Not every const in a scanned package is expressible in Dart
				// (durations, function calls). Only the ones the emitter asks
				// for must resolve, so an unresolvable value is simply absent —
				// and a requested-but-absent const fails in the emitter, where
				// the name is known and the error can say which one.
				continue
			}
			s.consts[pkgPath+"."+name.Name] = &constDecl{doc: doc, dart: dart, goSource: goSrc}
			group = append(group, name.Name)
		}
	}
	if len(group) > 0 {
		s.groups[pkgPath+"."+group[0]] = group
	}
	return nil
}

// constLit renders one constant expression as a Dart literal. It returns the Go
// source form alongside so the emitter can preserve a binary literal — Dart has
// no 0b syntax, and 0b0000_0100_0000 says "bit 6" in a way 0x40 does not.
func (s *source) constLit(pkgPath string, e ast.Expr) (dart, goSrc string, err error) {
	switch v := e.(type) {
	case *ast.BasicLit:
		switch v.Kind {
		case token.STRING:
			str, uerr := strconv.Unquote(v.Value)
			if uerr != nil {
				return "", "", uerr
			}
			return dartString(str), v.Value, nil
		case token.INT:
			clean := strings.ReplaceAll(v.Value, "_", "")
			n, perr := strconv.ParseUint(clean, 0, 64)
			if perr != nil {
				return "", "", perr
			}
			// Bit patterns stay in a base that shows bits; counts stay decimal.
			if strings.HasPrefix(clean, "0b") || strings.HasPrefix(clean, "0x") {
				return fmt.Sprintf("0x%X", n), v.Value, nil
			}
			return strconv.FormatUint(n, 10), v.Value, nil
		case token.FLOAT:
			return strings.ReplaceAll(v.Value, "_", ""), v.Value, nil
		}
	case *ast.UnaryExpr:
		if v.Op == token.SUB {
			inner, src, ierr := s.constLit(pkgPath, v.X)
			if ierr != nil {
				return "", "", ierr
			}
			return "-" + inner, "-" + src, nil
		}
	case *ast.Ident:
		if c, ok := s.consts[pkgPath+"."+v.Name]; ok {
			return c.dart, c.goSource, nil
		}
	}
	return "", "", fmt.Errorf("unsupported const expression")
}

// embeddedName is the field name Go gives an embedded field: the type's own
// name, with any pointer/qualifier stripped.
func embeddedName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return embeddedName(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

// docText flattens a comment group to plain text with the markers stripped,
// preserving line breaks so the Dart side can re-wrap it as ///.
func docText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.TrimRight(g.Text(), "\n")
}
