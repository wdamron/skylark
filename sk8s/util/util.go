// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package util

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Package byte

const (
	None Package = iota
	Core
	Apps
	Authentication
	Authorization
	Autoscaling
	Batch
	Networking
	Rbac
	Storage
	Resource
	Meta
	Types
	Intstr
	Apiextensions
)

var packagePaths = [...]string{
	Core:           "k8s.io/api/core/v1",
	Apps:           "k8s.io/api/apps/v1",
	Authentication: "k8s.io/api/authentication/v1",
	Authorization:  "k8s.io/api/authorization/v1",
	Autoscaling:    "k8s.io/api/autoscaling/v1",
	Batch:          "k8s.io/api/batch/v1",
	Networking:     "k8s.io/api/networking/v1",
	Rbac:           "k8s.io/api/rbac/v1",
	Storage:        "k8s.io/api/storage/v1",
	Resource:       "k8s.io/apimachinery/pkg/api/resource",
	Meta:           "k8s.io/apimachinery/pkg/apis/meta/v1",
	Types:          "k8s.io/apimachinery/pkg/types",
	Intstr:         "k8s.io/apimachinery/pkg/util/intstr",
	Apiextensions:  "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions",
}

var packageEnums = map[string]Package{}

func init() {
	for enum, path := range packagePaths {
		packageEnums[path] = Package(enum)
	}
}

// Path returns the import path for p
func (p Package) Path() string { return packagePaths[p] }

func PackageForPath(path string) Package { return packageEnums[path] }

type FieldSpec struct {
	FieldType  reflect.Type
	FieldIndex uint16
	Package    Package

	Inline, Omitempty, Primitive, Pointer, Slice bool // TODO(wdamron): pack flags into a uint8
}

type TypeSpec struct {
	Type           reflect.Type
	Fields, Inline map[string]FieldSpec
	GVK            metav1.GroupVersionKind
}
