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

type iface interface {
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
	if prim, ok := primitiveToSkylarkValue(rv); ok {
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

func getAttr(v reflect.Value, name string, fields, inline map[string]util.FieldSpec) (skylark.Value, error) {
	spec, ok := fields[name]
	if !ok {
		return skylark.None, nil // no such attr
	}
	var err error
	v, ok, err = ensureStruct(v)
	if err != nil || !ok {
		return skylark.None, err
	}
	if !spec.Inline {
		return fieldValue(v, spec, name)
	}
	// nested field lookup:
	spec, ok = inline[name]
	if !ok {
		return skylark.None, nil // no such attr
	}
	v, ok, err = ensureStruct(v.FieldByIndex([]int{int(spec.FieldIndex)}))
	if err != nil || !ok {
		return skylark.None, err
	}
	return fieldValue(v, spec, name)
}

func ensureStruct(v reflect.Value) (reflect.Value, bool, error) {
	t := v.Type()
	k := t.Kind()
	if k == reflect.Ptr {
		if v.IsNil() {
			return v, false, nil // no such attr
		}
		v = v.Elem()
		t = v.Type()
		k = t.Kind()
		if k != reflect.Struct {
			return v, false, skylark.TypeErrorf("expected struct type or pointer to struct type")
		}
	}
	return v, true, nil
}

func fieldValue(v reflect.Value, spec util.FieldSpec, name string) (skylark.Value, error) {
	field := v.FieldByIndex([]int{int(spec.FieldIndex)})
	if debugfieldtypes && field.Type() != spec.FieldType {
		return skylark.None, skylark.TypeErrorf("unexpected field type for embedded attribute %s", name)
	}
	if spec.Primitive {
		prim, ok := primitiveToSkylarkValue(field)
		if debugfieldtypes && !ok {
			return prim, skylark.TypeErrorf("unhandled primitive type %s for attribute %s", spec.FieldType.Name(), name)
		}
		return prim, nil
	}
	if cast, ok := g2s[spec.FieldType]; ok && cast != nil {
		return cast(field.Interface()), nil
	}
	return skylark.None, skylark.TypeErrorf("missing cast from type %s for attribute %s", spec.FieldType.Name(), name)
}

func isPrimitive(t reflect.Type) bool {
	switch reflect.New(t).Elem().Interface().(type) {
	case bool, *bool, int32, *int32, int64, *int64, float32, *float32, float64, *float64, string, *string, []string, *[]string,
		map[string]string, map[string][]byte, map[v1.ResourceName]resource.Quantity, metav1.Verbs, metav1.Time, *metav1.Time,
		metav1.MicroTime, *metav1.MicroTime, intstr.IntOrString, *intstr.IntOrString, resource.Quantity, *resource.Quantity:
		return true
	}
	switch t.Kind() {
	case reflect.String:
		return true
	case reflect.Ptr:
		if t.Elem().Kind() == reflect.String {
			return true
		}
	}
	return false
}

func primitiveToSkylarkValue(v reflect.Value) (skylark.Value, bool) {
	switch t := v.Interface().(type) {
	case bool:
		return skylark.Bool(t), true
	case *bool:
		if t == nil {
			return skylark.None, true
		}
		return skylark.Bool(*t), true
	case int32:
		return skylark.MakeInt64(int64(t)), true
	case *int32:
		if t == nil {
			return skylark.None, true
		}
		return skylark.MakeInt64(int64(*t)), true
	case int64:
		return skylark.MakeInt64(t), true
	case *int64:
		if t == nil {
			return skylark.None, true
		}
		return skylark.MakeInt64(*t), true
	case float32:
		return skylark.Float(float64(t)), true
	case *float32:
		if t == nil {
			return skylark.None, true
		}
		return skylark.Float(float64(*t)), true
	case float64:
		return skylark.Float(t), true
	case *float64:
		if t == nil {
			return skylark.None, true
		}
		return skylark.Float(*t), true
	case string:
		return skylark.String(t), true
	case *string:
		if t == nil {
			return skylark.None, true
		}
		return skylark.String(*t), true
	case []string:
		return toStringList(t), true
	case *[]string:
		if t == nil {
			return skylark.None, true
		}
		return toStringList(*t), true
	case map[string]string:
		if t == nil {
			return skylark.None, true
		}
		d := new(skylark.Dict)
		for k, v := range t {
			d.Set(skylark.String(k), skylark.String(v))
		}
		return d, true
	case map[string][]byte:
		if t == nil {
			return skylark.None, true
		}
		d := new(skylark.Dict)
		for k, v := range t {
			d.Set(skylark.String(k), skylark.String(base64.StdEncoding.EncodeToString(v)))
		}
		return d, true
	case map[v1.ResourceName]resource.Quantity:
		if t == nil {
			return skylark.None, true
		}
		d := new(skylark.Dict)
		for k, v := range t {
			d.Set(skylark.String(string(k)), skylark.String(v.String()))
		}
		return d, true
	case metav1.Verbs:
		return toStringList([]string(t)), true
	case metav1.Time:
		return skylark.MakeInt64(t.UnixNano()), true
	case *metav1.Time:
		if t == nil {
			return skylark.None, true
		}
		return skylark.MakeInt64((*t).UnixNano()), true
	case metav1.MicroTime:
		return skylark.MakeInt64(t.UnixNano()), true
	case *metav1.MicroTime:
		if t == nil {
			return skylark.None, true
		}
		return skylark.MakeInt64((*t).UnixNano()), true
	case intstr.IntOrString:
		switch t.Type {
		case intstr.Int:
			return skylark.MakeInt(t.IntValue()), true
		case intstr.String:
			return skylark.String(t.String()), true
		default:
			return skylark.None, true
		}
	case *intstr.IntOrString:
		if t == nil {
			return skylark.None, true
		}
		switch t.Type {
		case intstr.Int:
			return skylark.MakeInt(t.IntValue()), true
		case intstr.String:
			return skylark.String(t.String()), true
		default:
			return skylark.None, true
		}
	case resource.Quantity:
		return skylark.String(t.String()), true
	case *resource.Quantity:
		if t == nil {
			return skylark.None, true
		}
		return skylark.String(t.String()), true
	case nil:
		return skylark.None, true
	}

	rt := v.Type()
	kind := rt.Kind()
	switch kind {
	case reflect.String:
		return skylark.String(v.Interface().(string)), true
	case reflect.Ptr:
		if rt.Elem().Kind() == reflect.String {
			if v.IsNil() {
				return skylark.None, true
			}
			p := v.Interface().(*string)
			return skylark.String(*p), true
		}
	}

	return skylark.None, false
}

func genericStringMethod(v interface{}) string {
	return fmt.Sprintf("%#+v", v)
}

func toStringList(ss []string) *skylark.List {
	elems := make([]skylark.Value, len(ss))
	for i, s := range ss {
		elems[i] = skylark.String(s)
	}
	return skylark.NewList(elems)
}

// called by generated code:

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
