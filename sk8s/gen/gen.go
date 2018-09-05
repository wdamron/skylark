// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"go/importer"
	"go/types"
	"log"
	"strings"
	"text/template"

	"github.com/google/skylark/sk8s/util"
)

var packages = []string{
	"k8s.io/api/core/v1",
	"k8s.io/apimachinery/pkg/apis/meta/v1",
}

var packageAliases = map[string]string{
	"k8s.io/api/core/v1":                   "v1",
	"k8s.io/apimachinery/pkg/apis/meta/v1": "metav1",
	"k8s.io/apimachinery/pkg/api/resource": "resource",
	"k8s.io/apimachinery/pkg/util/intstr":  "intstr",
}

var primitives = map[string]bool{
	"Time":      true,
	"Timestamp": true,
	"MicroTime": true,
}

func main() {

	out := &bytes.Buffer{}
	out.WriteString(header)

	imported := make(map[util.Package]*types.Package)
	for _, path := range packages {
		p, err := importer.Default().Import(path)
		if err != nil {
			log.Fatal(err)
		}
		imported[util.PackageForPath(path)] = p
	}

	written := make(map[string]bool)
	for _, path := range packages {
		if err := writeTypes(out, util.PackageForPath(path), imported, written); err != nil {
			log.Fatal(err)
		}
	}

	fmt.Println(string(out.Bytes()))
}

func writeTypes(out *bytes.Buffer, pkg util.Package, imported map[util.Package]*types.Package, written map[string]bool) error {

	type Data struct {
		TypeName                *types.TypeName
		Pkg                     util.Package
		PkgName, PkgAlias, Name string
		Type                    *types.Struct
		Imports                 map[util.Package]*types.Package
	}

	scope := imported[pkg].Scope()

	for _, name := range scope.Names() {
		if written[name] || primitives[name] {
			continue
		}

		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}

		typeName, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}

		path := typeName.Pkg().Path()
		pkgAlias := packageAliases[path]

		if stype, ok := obj.Type().Underlying().(*types.Struct); ok {
			data := Data{
				TypeName: typeName,
				Pkg:      util.PackageForPath(path),
				PkgName:  path,
				PkgAlias: pkgAlias,
				Name:     name,
				Type:     stype,
				Imports:  imported,
			}

			if err := body.Execute(out, data); err != nil {
				return err
			}

			written[name] = true
		}
	}

	return nil
}

func hasstringmethod(tn *types.TypeName) bool {
	n := tn.Type().(*types.Named)
	nm := n.NumMethods()
	for i := 0; i < nm; i++ {
		m := n.Method(i)
		if m.Name() == "String" {
			return true
		}
	}
	return false
}

var funcs = template.FuncMap{
	"titlecase":       strings.Title,
	"hasstringmethod": hasstringmethod,
}

const header = `// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kinds

// Code generated by sk8s/gen. DO NOT EDIT

import (
	"reflect"

	"github.com/google/skylark"
	"github.com/google/skylark/sk8s/util"
	"github.com/google/skylark/syntax"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)`

var body = template.Must(template.New("sk8s_generated_body").Funcs(funcs).Parse(`
{{ $pkg := .PkgAlias }}
{{ $name := .Name }}
{{ $t := .Type }}
{{ $tn := .TypeName }}
{{ $imports := .Imports }}

type {{ $name }} struct {
	V *{{ $pkg }}.{{ $name }}
}

var (
	_ iface = (*{{ $name }})(nil)

	{{ $name }}_fields = map[string]util.FieldSpec{}
	{{ $name }}_inline = map[string]util.FieldSpec{}
	{{ $name }}_attrs []string
)

func init() {
	t := reflect.TypeOf((*{{ $pkg }}.{{ $name }})(nil)).Elem()
	g2s[t] = func(iface interface{}) skylark.Value {
		switch v := iface.(type) {
		case *{{ $pkg }}.{{ $name }}:
			return {{ $name }}{V: v}
		case {{ $pkg }}.{{ $name }}:
			return {{ $name }}{V: &v}
		default:
			return skylark.None
		}
	}
	{{ $name }}_attrs = setFieldTypes(t, {{ $name }}_fields, {{ $name }}_inline)
	Library["{{ $name }}"] = skylark.NewBuiltin("{{ $name }}", create{{ $name }})
}

func create{{ $name }}(thread *skylark.Thread, fn *skylark.Builtin, args skylark.Tuple, kwargs []skylark.Tuple) (skylark.Value, error) {
	return nil, nil // TODO: add constructor for {{ $name }}
}
func (t {{ $name }}) UnderlyingKind() interface{} { return t.V }
func (t {{ $name }}) Package() util.Package  { return util.{{ $pkg | titlecase }} }
func (t {{ $name }}) Type() string        { return "k8s_{{ $pkg }}_{{ $name }}" }
func (t {{ $name }}) String() string { return {{ if ( hasstringmethod $tn ) }} t.V.String() {{ else }} genericStringMethod(t.V) {{ end }} }
func (t {{ $name }}) Freeze()             { } // TODO
func (t {{ $name }}) Truth() skylark.Bool { return skylark.True }
func (t {{ $name }}) Hash() (uint32, error) { return 0, unhashable(t.Type()) }
func (t {{ $name }}) CompareSameType(op syntax.Token, y_ skylark.Value, depth int) (bool, error) {
	y := y_.(*{{ $name }})
	return compareSameType(t, op, y, t.Type(), depth)
}
func (t {{ $name }}) AttrNames() []string { return {{ $name }}_attrs }
func (t {{ $name }}) Attr(name string) (skylark.Value, error) {
	if u := t.V; u != nil {
		return getAttr(reflect.ValueOf(u), name, {{ $name }}_fields, {{ $name }}_inline)
	}
	return skylark.None, uninitialized(t.Type(), name)
}`))