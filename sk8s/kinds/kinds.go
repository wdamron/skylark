// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kinds

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/skylark"
	"github.com/google/skylark/sk8s/util"
	"github.com/google/skylark/syntax"

	v1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
)

const debugfieldtypes = true

var (
	Library = map[string]*skylark.Builtin{}
	g2s     = map[reflect.Type]func(interface{}) skylark.Value{}
)

// This interface is also defined in the parent directory, but is included here for type assertions
// within the generated code, and to avoid a circular dependency.
type boxed interface {
	skylark.HasAttrs
	UnderlyingKind() interface{}
}

func ToSky(v interface{}) (skylark.Value, error) {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	rk := rt.Kind()
	if rk == reflect.Ptr {
		if rv.IsNil() {
			return skylark.None, nil
		}
		rt = rt.Elem()
	}
	if prim, ok := primitiveToSkylarkValue(rv, rt); ok {
		return prim, nil
	} else if cast, ok := g2s[rt]; ok {
		return cast(v), nil
	}
	return skylark.None, nil
}

func compareSameType(x interface{}, op syntax.Token, y interface{}, t string, depth int) (bool, error) {
	switch op {
	case syntax.EQL:
		return reflect.DeepEqual(x, y), nil
	case syntax.NEQ:
		return !reflect.DeepEqual(x, y), nil
	default:
		return false, skylark.TypeErrorf("%s %s %s not implemented", t, op, t)
	}
}

func getAttr(container reflect.Value, name string, fields, inline map[string]util.FieldSpec) (skylark.Value, error) {
	spec, ok := fields[name]
	if !ok {
		return skylark.None, nil // no such attr
	}
	var err error
	container, ok, err = derefStruct(container)
	if err != nil || !ok {
		return skylark.None, err
	}
	if !spec.Inline {
		return fieldValue(container, spec, name)
	}
	// nested field lookup:
	spec, ok = inline[name]
	if !ok {
		return skylark.None, nil // no such attr
	}
	container, ok, err = derefStruct(container.FieldByIndex([]int{int(spec.FieldIndex)}))
	if err != nil || !ok {
		return skylark.None, err
	}
	return fieldValue(container, spec, name)
}

func derefStruct(v reflect.Value) (reflect.Value, bool, error) {
	t := v.Type()
	k := t.Kind()
	if k == reflect.Ptr {
		if v.IsNil() {
			return v, false, nil // no such attr
		}
		v = v.Elem()
		t = v.Type()
		k = t.Kind()
		if debugfieldtypes && k != reflect.Struct {
			return v, false, skylark.TypeErrorf("expected struct type or pointer to struct type, found %s", t.Name())
		}
	}
	return v, true, nil
}

func fieldValue(container reflect.Value, spec util.FieldSpec, name string) (skylark.Value, error) {
	field := container.FieldByIndex([]int{int(spec.FieldIndex)})
	if debugfieldtypes && field.Type() != spec.FieldType {
		return skylark.None, skylark.TypeErrorf("unexpected field type for embedded attribute %s", name)
	}
	if spec.Primitive {
		prim, ok := primitiveToSkylarkValue(field, spec.FieldType)
		if debugfieldtypes && !ok {
			return prim, skylark.TypeErrorf("unhandled primitive type %s for attribute %s", spec.FieldType.Name(), name)
		}
		return prim, nil
	}
	if spec.Slice {
		slice, ok := sliceToSkylarkValue(field, spec.FieldType)
		if debugfieldtypes && !ok {
			return slice, skylark.TypeErrorf("unhandled slice type %s for attribute %s", spec.FieldType.Name(), name)
		}
		return slice, nil
	}
	if cast, ok := g2s[spec.FieldType]; ok && cast != nil {
		return cast(field.Interface()), nil
	}
	return skylark.None, skylark.TypeErrorf("missing cast from type %s for attribute %s", spec.FieldType.Name(), name)
}

var commonTypes = map[reflect.Type]bool{
	reflect.TypeOf(([]string)(nil)):                              true,
	reflect.TypeOf((map[string]string)(nil)):                     true,
	reflect.TypeOf((map[string][]byte)(nil)):                     true,
	reflect.TypeOf((map[v1.ResourceName]resource.Quantity)(nil)): true,
	reflect.TypeOf((metav1.Verbs)(nil)):                          true,
	reflect.TypeOf(metav1.Time{}):                                true,
	reflect.TypeOf(metav1.MicroTime{}):                           true,
	reflect.TypeOf(intstr.IntOrString{}):                         true,
	reflect.TypeOf(resource.Quantity{}):                          true,
}

func isPrimitive(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64, reflect.String:
		return true
	}
	if commonTypes[t] {
		return true
	}
	return false
}

var stringType = reflect.TypeOf("")

func toSkylarkString(v interface{}) skylark.Value {
	return skylark.String(string(reflect.ValueOf(v).Convert(stringType).Interface().(string)))
}

func sliceToSkylarkValue(slice reflect.Value, t reflect.Type) (skylark.Value, bool) {
	if t.Kind() == reflect.Ptr {
		if slice.IsNil() {
			return skylark.None, true
		}
		slice = slice.Elem()
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice {
		return skylark.None, false
	}
	t = t.Elem()
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	cast, ok := g2s[t]
	if !ok {
		if t.Kind() != reflect.String {
			return skylark.None, false
		}
		// Some types embed a slice of a named type with string as its underlying kind;
		// []string is handled as a primitive elsewhere:
		cast = toSkylarkString
	}
	n := slice.Len()
	elems := make([]skylark.Value, n)
	for i := 0; i < n; i++ {
		elems[i] = cast(slice.Index(i).Interface())
	}
	return skylark.NewList(elems), true
}

func primitiveToSkylarkValue(r reflect.Value, t reflect.Type) (skylark.Value, bool) {
	if t.Kind() == reflect.Ptr {
		if r.IsNil() {
			return skylark.None, true
		}
		r = r.Elem()
		t = t.Elem()
	}
	switch v := r.Interface().(type) {
	case bool:
		return skylark.Bool(v), true
	case int32:
		return skylark.MakeInt64(int64(v)), true
	case int64:
		return skylark.MakeInt64(v), true
	case float32:
		return skylark.Float(float64(v)), true
	case float64:
		return skylark.Float(v), true
	case string:
		return skylark.String(v), true
	case []string:
		return toStringList(v), true
	case map[string]string:
		if v == nil {
			return skylark.None, true
		}
		d := new(skylark.Dict)
		for key, val := range v {
			d.Set(skylark.String(key), skylark.String(val))
		}
		return d, true
	case map[string][]byte:
		if v == nil {
			return skylark.None, true
		}
		d := new(skylark.Dict)
		for key, val := range v {
			d.Set(skylark.String(key), skylark.String(base64.StdEncoding.EncodeToString(val)))
		}
		return d, true
	case map[v1.ResourceName]resource.Quantity:
		if v == nil {
			return skylark.None, true
		}
		d := new(skylark.Dict)
		for key, val := range v {
			d.Set(skylark.String(string(key)), skylark.String(val.String()))
		}
		return d, true
	case metav1.Verbs:
		return toStringList([]string(v)), true
	case metav1.Time:
		return skylark.MakeInt64(v.UnixNano()), true
	case metav1.MicroTime:
		return skylark.MakeInt64(v.UnixNano()), true
	case intstr.IntOrString:
		switch v.Type {
		case intstr.Int:
			return skylark.MakeInt(v.IntValue()), true
		case intstr.String:
			return skylark.String(v.String()), true
		default:
			return skylark.None, true
		}
	case resource.Quantity:
		return skylark.String(v.String()), true
	case nil:
		return skylark.None, true
	default:
		if t.Kind() == reflect.String {
			return toSkylarkString(v), true
		}
	}

	return skylark.None, false
}

func toStringList(ss []string) *skylark.List {
	elems := make([]skylark.Value, len(ss))
	for i, s := range ss {
		elems[i] = skylark.String(s)
	}
	return skylark.NewList(elems)
}

// returns a list of attribute names
func setFieldTypes(t reflect.Type, fields, inlineFields map[string]util.FieldSpec) []string {
	n := t.NumField()
	if n == 0 {
		return []string{}
	}
	names := make(map[string]bool)
	parseTag := func(tag string) (string, bool, bool) {
		var inline, omitempty bool
		if tag == "" || tag == "_" {
			return "", inline, omitempty
		}
		parts := strings.Split(tag, ",")
		jsonName := parts[0]
		if jsonName == "_" {
			return "", inline, omitempty
		}
		for _, part := range parts[1:] {
			switch part {
			case "inline":
				inline = true
			case "omitempty":
				omitempty = true
			}
		}
		return jsonName, inline, omitempty
	}
	for i := 0; i < n; i++ {
		field := t.FieldByIndex([]int{i})
		kind := field.Type.Kind()
		jsonName, inline, omitempty := parseTag(field.Tag.Get("json"))
		if jsonName == "" && !inline {
			continue
		}
		primitive := !inline && isPrimitive(field.Type)
		pointer := kind == reflect.Ptr
		slice := kind == reflect.Slice || (pointer && field.Type.Elem().Kind() == reflect.Slice)
		spec := util.FieldSpec{
			FieldType:  field.Type,
			FieldIndex: uint8(i),
			Package:    util.PackageForPath(field.PkgPath),
			Inline:     inline,
			Omitempty:  omitempty,
			Primitive:  primitive,
			Pointer:    pointer,
			Slice:      slice,
		}
		if !inline {
			names[jsonName] = true
			fields[jsonName] = spec
			continue
		}
		ft := field.Type
		if pointer {
			ft = ft.Elem()
		}
		m := ft.NumField()
		for j := 0; j < m; j++ {
			field := ft.FieldByIndex([]int{j})
			kind := field.Type.Kind()
			jsonName, inline, omitempty := parseTag(field.Tag.Get("json"))
			if jsonName == "" || inline {
				continue
			}
			primitive := isPrimitive(field.Type)
			pointer := kind == reflect.Ptr
			slice := kind == reflect.Slice || (pointer && field.Type.Elem().Kind() == reflect.Slice)
			inlineSpec := util.FieldSpec{
				FieldType:  field.Type,
				FieldIndex: uint8(j),
				Package:    util.PackageForPath(field.PkgPath),
				Inline:     false,
				Omitempty:  omitempty,
				Primitive:  primitive,
				Pointer:    pointer,
				Slice:      slice,
			}
			names[jsonName] = true
			inlineFields[jsonName] = inlineSpec
			fields[jsonName] = spec
		}
	}
	if len(names) == 0 {
		return []string{}
	}
	attrs := make([]string, 0, len(names))
	for k, _ := range names {
		attrs = append(attrs, k)
	}
	sort.Strings(attrs)
	return attrs
}

// called by generated code:

func genericStringMethod(v interface{}) string {
	return fmt.Sprintf("%#+v", v)
}

func unhashable(kind string) error {
	return skylark.TypeErrorf("unhashable type: " + kind)
}

func uninitialized(kind, attr string) error {
	return skylark.ValueErrorf("can not set attribute \"%s\" in uninitialized %s", attr, kind)
}
func isfrozen(kind, attr string) error {
	return skylark.ValueErrorf("can not set attribute \"%s\" in frozen %s", attr, kind)
}
func cantset(kind, attr string) error {
	return skylark.ValueErrorf("can not set attribute \"%s\" in %s", attr, kind)
}
func missingattr(kind, attr string) error {
	return skylark.ValueErrorf("missing attribute \"%s\" in %s", attr, kind)
}
func cantseterr(kind, attr string, err error) error {
	return skylark.ValueErrorf("can not set attribute \"%s\" in %s: %s", attr, kind, err.Error())
}
