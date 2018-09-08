// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kinds

import (
	"encoding/base64"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

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
	skylark.HasSetField
	Underlying() interface{}
	DeepCopy() boxed
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

func construct(box boxed, args skylark.Tuple, kwargs []skylark.Tuple) error {
	if len(args) > 1 {
		return skylark.TypeErrorf("unable to construct %s from more than 1 positional argument", box.Type())
	}
	if len(args) != 0 {
		switch arg := args[0].(type) {
		case *skylark.Dict:
			iter := arg.Iterate()
			defer iter.Done()
			var k skylark.Value
			for iter.Next(&k) {
				ks, ok := k.(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %v key in %v", k.Type(), box.Type())
				}
				v, _, _ := arg.Get(ks)
				if err := box.SetField(string(ks), v); err != nil {
					return err
				}
			}
		case boxed:
			if arg.Type() != box.Type() {
				return skylark.TypeErrorf("unable to construct %s from %s", box.Type(), arg.Type())
			}
			v := reflect.ValueOf(arg.DeepCopy().Underlying()).Elem()
			reflect.ValueOf(box.Underlying()).Elem().Set(v)
		}
	}
	for _, pair := range kwargs {
		k, _ := pair[0].(skylark.String)
		if err := box.SetField(string(k), pair[1]); err != nil {
			return err
		}
	}
	return nil
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

func setAttr(box boxed, name string, value skylark.Value, fields, inline map[string]util.FieldSpec) error {
	spec, ok := fields[name]
	if !ok {
		return skylark.TypeErrorf("unable to set field %s in type %s", name, box.Type())
	}
	container, ok, err := derefStruct(reflect.ValueOf(box.Underlying()))
	if err != nil {
		return skylark.TypeErrorf("unable to set field %s in type %s: %v", name, box.Type(), err)
	}
	if !ok {
		return skylark.TypeErrorf("unable to set field %s in type %s", name, box.Type())
	}
	if !spec.Inline {
		return setFieldValue(container, spec, name, value)
	}
	spec, ok = inline[name]
	if !ok {
		return skylark.TypeErrorf("unable to set field %s in type %s", name, box.Type())
	}
	container, ok, err = derefStruct(container.FieldByIndex([]int{int(spec.FieldIndex)}))
	if err != nil {
		return skylark.TypeErrorf("unable to set field %s in type %s: %v", name, box.Type(), err)
	}
	if !ok {
		return skylark.TypeErrorf("unable to set field %s in type %s", name, box.Type())
	}
	return setFieldValue(container, spec, name, value)
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
		return skylark.None, skylark.TypeErrorf("unexpected field type for attribute %s", name)
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
		if !spec.Pointer && field.CanAddr() {
			field = field.Addr()
		}
		return cast(field.Interface()), nil
	}
	return skylark.None, skylark.TypeErrorf("missing cast from type %s for attribute %s", spec.FieldType.Name(), name)
}

func setFieldValue(container reflect.Value, spec util.FieldSpec, name string, value skylark.Value) error {
	field := container.FieldByIndex([]int{int(spec.FieldIndex)})
	if debugfieldtypes && spec.FieldType != field.Type() {
		return skylark.TypeErrorf("unexpected field type %s for attribute %s", spec.FieldType.Name(), name)
	}
	if debugfieldtypes && !field.CanSet() {
		return skylark.TypeErrorf("unable to set field %s of type %s", name, spec.FieldType.Name())
	}
	if _, ok := value.(skylark.NoneType); ok {
		field.Set(reflect.Zero(spec.FieldType))
		return nil
	}
	if spec.Primitive {
		return setPrimitiveField(field, value, spec)
	}
	if spec.Slice {
		return setSliceField(field, value, spec)
	}
	if debugfieldtypes {
		if (spec.Pointer && spec.FieldType.Elem().Kind() != reflect.Struct) || spec.FieldType.Kind() != reflect.Struct {
			return skylark.TypeErrorf("unable to set field %s of type %s", name, spec.FieldType.Name())
		}
	}
	var u reflect.Value
	switch v := value.(type) {
	case boxed:
		u = reflect.ValueOf(v.Underlying())
	case *skylark.Dict:
		t := spec.FieldType
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if box, ok := g2s[spec.FieldType](reflect.New(t).Elem().Interface()).(boxed); ok {
			var k skylark.Value
			iter := v.Iterate()
			defer iter.Done()
			for iter.Next(&k) {
				attrName, ok := k.(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %v key in %v", k.Type(), box.Type())
				}
				attrValue, _, _ := v.Get(attrName)
				if err := box.SetField(string(attrName), attrValue); err != nil {
					return err
				}
			}
			u = reflect.ValueOf(box.Underlying())
		} else {
			return skylark.TypeErrorf("unable to set field %s from type %s to type %s", name, v.Type(), spec.FieldType.Name())
		}
	default:
		return skylark.TypeErrorf("unable to set field %s from type %s", name, v.Type())
	}
	if spec.Pointer {
		field.Set(u)
	} else if u.IsNil() {
		field.Set(reflect.Zero(spec.FieldType))
	} else {
		field.Set(u.Elem())
	}
	return nil
}

var (
	emptyValue = reflect.ValueOf(false)
	trueValue  = reflect.ValueOf(true)
	falseValue = reflect.ValueOf(false)
)

var boolType = trueValue.Type()
var int32Type = reflect.TypeOf(int32(0))
var int64Type = reflect.TypeOf(int64(0))
var float32Type = reflect.TypeOf(float32(0.0))
var float64Type = reflect.TypeOf(float64(0.0))
var stringType = reflect.TypeOf("")
var sliceStringType = reflect.TypeOf(([]string)(nil))
var verbsSliceType = reflect.TypeOf((metav1.Verbs)(nil))
var mapStringStringType = reflect.TypeOf((map[string]string)(nil))
var mapStringSliceByteType = reflect.TypeOf((map[string][]byte)(nil))
var mapResourceNameQuantityType = reflect.TypeOf((map[v1.ResourceName]resource.Quantity)(nil))
var timeType = reflect.TypeOf(metav1.Time{})
var microTimeType = reflect.TypeOf(metav1.MicroTime{})
var intOrStringType = reflect.TypeOf(intstr.IntOrString{})
var quantityType = reflect.TypeOf(resource.Quantity{})

var commonTypes = map[reflect.Type]bool{
	boolType:                    true,
	int32Type:                   true,
	int64Type:                   true,
	float32Type:                 true,
	float64Type:                 true,
	stringType:                  true,
	sliceStringType:             true,
	mapStringStringType:         true,
	mapStringSliceByteType:      true,
	mapResourceNameQuantityType: true,
	verbsSliceType:              true,
	timeType:                    true,
	microTimeType:               true,
	intOrStringType:             true,
	quantityType:                true,
}

func isPrimitive(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return commonTypes[t]
}

func toSkylarkString(v interface{}) skylark.Value {
	return skylark.String(string(reflect.ValueOf(v).Convert(stringType).Interface().(string)))
}

func toStringList(ss []string) *skylark.List {
	elems := make([]skylark.Value, len(ss))
	for i, s := range ss {
		elems[i] = skylark.String(s)
	}
	return skylark.NewList(elems)
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
		// Some types embed a slice of a named string type; []string is handled as a primitive elsewhere:
		cast = toSkylarkString
	}
	n := slice.Len()
	elems := make([]skylark.Value, n)
	for i := 0; i < n; i++ {
		elems[i] = cast(slice.Index(i).Interface())
	}
	return skylark.NewList(elems), true
}

func setSliceField(field reflect.Value, value skylark.Value, spec util.FieldSpec) error {
	t := spec.FieldType
	if spec.Pointer {
		t = t.Elem()
	}
	if debugfieldtypes && t.Kind() != reflect.Slice {
		return skylark.TypeErrorf("unable to assign %s to %s", value.Type(), t.Kind())
	}
	elem := t.Elem()
	list, ok := value.(*skylark.List)
	if !ok {
		return skylark.TypeErrorf("unable to assign %s to slice of %s", value.Type(), elem.Name())
	}
	length := list.Len()
	slice := reflect.MakeSlice(t, length, length)
	switch elem.Kind() {
	// Some types embed a slice of a named string type:
	case reflect.String:
		for i := 0; i < length; i++ {
			s, ok := list.Index(i).(skylark.String)
			if !ok {
				return skylark.TypeErrorf("unable to assign %s to element of string slice", list.Index(i).Type())
			}
			slice.Index(i).Set(reflect.ValueOf(s).Convert(elem))
		}
	case reflect.Struct:
		for i := 0; i < length; i++ {
			b, ok := list.Index(i).(boxed)
			if !ok {
				return skylark.TypeErrorf("unable to assign %s to element of slice", list.Index(i).Type())
			}
			slice.Index(i).Set(reflect.ValueOf(b.Underlying()).Elem())
		}
	default:
		if debugfieldtypes {
			return skylark.TypeErrorf("unable to assign %s to slice of %s", value.Type(), elem.Name())
		}
		return nil
	}
	if spec.Pointer {
		field.Set(slice.Addr())
	} else {
		field.Set(slice)
	}
	return nil
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
	case metav1.Verbs:
		return toStringList([]string(v)), true
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
		// Some types embed a slice of a named string type:
		if t.Kind() == reflect.String {
			return toSkylarkString(v), true
		}
	}

	return skylark.None, false
}

func setPrimitiveField(field reflect.Value, value skylark.Value, spec util.FieldSpec) error {
	t := spec.FieldType
	elem := t
	if spec.Pointer {
		elem = t.Elem()
	}

	switch elem {
	case boolType:
		switch v := value.(type) {
		case skylark.Bool:
			b := bool(v)
			if spec.Pointer {
				field.Set(reflect.ValueOf(&b))
			} else {
				field.Set(reflect.ValueOf(b))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to bool", v.Type())
		}
	case int32Type, int64Type:
		switch v := value.(type) {
		case skylark.Int:
			i, ok := v.Int64()
			if !ok {
				return skylark.ValueErrorf("value exceeds the range of int64")
			}
			switch elem.Kind() {
			case reflect.Int32:
				if i < math.MinInt32 || i > math.MaxInt32 {
					return skylark.ValueErrorf("value exceeds the range of int32")
				}
				i32 := int32(i)
				if spec.Pointer {
					field.Set(reflect.ValueOf(&i32))
				} else {
					field.Set(reflect.ValueOf(i32))
				}
			case reflect.Int64:
				if spec.Pointer {
					field.Set(reflect.ValueOf(&i))
				} else {
					field.Set(reflect.ValueOf(i))
				}
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to %v", v.Type(), elem.Kind().String())
		}
	case float32Type, float64Type:
		switch v := value.(type) {
		case skylark.Float:
			f := float64(v)
			switch elem.Kind() {
			case reflect.Float32:
				if v > math.MaxFloat32 {
					return skylark.ValueErrorf("value exceeds the range of float32")
				}
				f32 := float32(f)
				if spec.Pointer {
					field.Set(reflect.ValueOf(&f32))
				} else {
					field.Set(reflect.ValueOf(f32))
				}
			case reflect.Float64:
				if spec.Pointer {
					field.Set(reflect.ValueOf(&f))
				} else {
					field.Set(reflect.ValueOf(f))
				}
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to %v", v.Type(), elem.Kind().String())
		}
	case stringType:
		switch v := value.(type) {
		case skylark.String:
			s := string(v)
			if spec.Pointer {
				field.Set(reflect.ValueOf(&s))
			} else {
				field.Set(reflect.ValueOf(s))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to string", v.Type())
		}
	case sliceStringType:
		switch v := value.(type) {
		case *skylark.List:
			if v == nil || v.Len() == 0 {
				field.Set(reflect.ValueOf(([]string)(nil)))
				return nil
			}
			length := v.Len()
			slice := make([]string, length)
			for i := 0; i < length; i++ {
				vi := v.Index(i)
				s, ok := vi.(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s to element of string slice", vi.Type())
				}
				slice[i] = string(s)
			}
			if spec.Pointer {
				field.Set(reflect.ValueOf(&slice))
			} else {
				field.Set(reflect.ValueOf(slice))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to string slice", v.Type())
		}
	case verbsSliceType:
		switch v := value.(type) {
		case *skylark.List:
			if v == nil || v.Len() == 0 {
				field.Set(reflect.ValueOf((metav1.Verbs)(nil)))
				return nil
			}
			length := v.Len()
			slice := make(metav1.Verbs, length)
			for i := 0; i < length; i++ {
				vi := v.Index(i)
				s, ok := vi.(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s to element of Verbs slice", vi.Type())
				}
				slice[i] = string(s)
			}
			if spec.Pointer {
				field.Set(reflect.ValueOf(&slice))
			} else {
				field.Set(reflect.ValueOf(slice))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to Verbs slice", v.Type())
		}
	case mapStringStringType:
		switch v := value.(type) {
		case *skylark.Dict:
			if v == nil || v.Len() == 0 {
				field.Set(reflect.ValueOf((map[string]string)(nil)))
				return nil
			}
			items := v.Items()
			m := make(map[string]string, len(items))
			for _, pair := range items {
				k, ok := pair[0].(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s as string key in map", k.Type())
				}
				v, ok := pair[1].(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s to string value in map", k.Type())
				}
				m[string(k)] = string(v)
			}
			field.Set(reflect.ValueOf(m))
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to map of string to string", v.Type())
		}
	case mapStringSliceByteType:
		switch v := value.(type) {
		case *skylark.Dict:
			if v == nil || v.Len() == 0 {
				field.Set(reflect.ValueOf((map[string][]byte)(nil)))
				return nil
			}
			items := v.Items()
			m := make(map[string][]byte, len(items))
			for _, pair := range items {
				k, ok := pair[0].(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s as string key in map", k.Type())
				}
				v, ok := pair[1].(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s to byte slice value in map", k.Type())
				}
				raw, err := base64.StdEncoding.DecodeString(string(v))
				if err != nil {
					return skylark.ValueErrorf("error decoding map value: %v", err)
				}
				m[string(k)] = raw
			}
			field.Set(reflect.ValueOf(m))
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to map of string to byte slice", v.Type())
		}
	case mapResourceNameQuantityType:
		switch v := value.(type) {
		case *skylark.Dict:
			if v == nil || v.Len() == 0 {
				field.Set(reflect.ValueOf((map[v1.ResourceName]resource.Quantity)(nil)))
				return nil
			}
			items := v.Items()
			m := make(map[v1.ResourceName]resource.Quantity, len(items))
			for _, pair := range items {
				k, ok := pair[0].(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s as string key in map", k.Type())
				}
				v, ok := pair[1].(skylark.String)
				if !ok {
					return skylark.TypeErrorf("unable to assign %s to string value in map", k.Type())
				}
				q, err := resource.ParseQuantity(string(v))
				if err != nil {
					return skylark.ValueErrorf("error decoding map value: %v", err)
				}
				m[v1.ResourceName(k)] = q
			}
			field.Set(reflect.ValueOf(m))
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to map of resource name to quantity", v.Type())
		}
	case timeType:
		switch v := value.(type) {
		case skylark.Int:
			i, ok := v.Int64()
			if !ok {
				return skylark.ValueErrorf("value exceeds the range of int64")
			}
			unix := metav1.Time{Time: time.Unix(0, i)}
			if spec.Pointer {
				field.Set(reflect.ValueOf(&unix))
			} else {
				field.Set(reflect.ValueOf(unix))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to time", v.Type())
		}
	case microTimeType:
		switch v := value.(type) {
		case skylark.Int:
			i, ok := v.Int64()
			if !ok {
				return skylark.ValueErrorf("value exceeds the range of int64")
			}
			unix := metav1.MicroTime{Time: time.Unix(0, i)}
			if spec.Pointer {
				field.Set(reflect.ValueOf(&unix))
			} else {
				field.Set(reflect.ValueOf(unix))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to micro time", v.Type())
		}
	case intOrStringType:
		switch v := value.(type) {
		case skylark.Int:
			i64, ok := v.Int64()
			if !ok || i64 < math.MinInt32 || i64 > math.MaxInt32 {
				return skylark.ValueErrorf("value exceeds the range of int32")
			}
			i := intstr.FromInt(int(i64))
			if spec.Pointer {
				field.Set(reflect.ValueOf(&i))
			} else {
				field.Set(reflect.ValueOf(i))
			}
			return nil
		case skylark.String:
			s := intstr.FromString(string(v))
			if spec.Pointer {
				field.Set(reflect.ValueOf(&s))
			} else {
				field.Set(reflect.ValueOf(s))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to int or string", v.Type())
		}
	case quantityType:
		switch v := value.(type) {
		case skylark.String:
			q, err := resource.ParseQuantity(string(v))
			if err != nil {
				return skylark.ValueErrorf("unable to assign string to quantity: %v", err)
			}
			if spec.Pointer {
				field.Set(reflect.ValueOf(&q))
			} else {
				field.Set(reflect.ValueOf(q))
			}
			return nil
		default:
			return skylark.TypeErrorf("unable to assign %v to quantity", v.Type())
		}
	default:
		switch v := value.(type) {
		// Some types embed a slice of a named string type:
		case skylark.String:
			switch elem.Kind() {
			case reflect.String:
				e := reflect.ValueOf(string(v)).Convert(elem)
				if spec.Pointer {
					field.Set(e.Addr())
				} else {
					field.Set(e)
				}
				return nil
			default:
				return skylark.TypeErrorf("unable to assign %v to string", v.Type())
			}
		}
	}
	return skylark.TypeErrorf("unhandled assignment from %v to %v", value.Type(), elem.Name())
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
		if tag == "" || tag == "-" {
			return "", inline, omitempty
		}
		parts := strings.Split(tag, ",")
		jsonName := parts[0]
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
