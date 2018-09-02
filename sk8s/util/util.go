// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package util

import (
	"reflect"
)

type Package byte

const (
	V1 Package = iota + 1
	Resource
	Metav1
	Types
	Intstr
	Other
)

var packagePaths = [...]string{
	V1:       "k8s.io/api/core/v1",
	Resource: "k8s.io/apimachinery/pkg/api/resource",
	Metav1:   "k8s.io/apimachinery/pkg/apis/meta/v1",
	Types:    "k8s.io/apimachinery/pkg/types",
	Intstr:   "k8s.io/apimachinery/pkg/util/intstr",
}

var packageEnums = map[string]Package{
	"k8s.io/api/core/v1":                   V1,
	"k8s.io/apimachinery/pkg/api/resource": Resource,
	"k8s.io/apimachinery/pkg/apis/meta/v1": Metav1,
	"k8s.io/apimachinery/pkg/types":        Types,
	"k8s.io/apimachinery/pkg/util/intstr":  Intstr,
}

// Path returns the import path for p
func (p Package) Path() string { return packagePaths[p] }

func PackageForPath(path string) Package { return packageEnums[path] }

type FieldSpec struct {
	FieldType  reflect.Type
	FieldIndex uint8
	Package    Package

	Inline, Omitempty, Primitive, Pointer, Slice bool // TODO(wdamron): pack flags into a uint8
}
