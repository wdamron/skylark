// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// package sk8s provides Skylark value implementations for Kubernetes API types.
//
// See the following packages:
// 		* godoc.org/k8s.io/api/core/v1
// 		* godoc.org/k8s.io/apimachinery/pkg/apis/meta/v1
// 		* godoc.org/k8s.io/apimachinery/pkg/api/resource
// 		* godoc.org/k8s.io/apimachinery/pkg/util/intstr
package sk8s

//go:generate bash ./gen/gen.sh

import (
	"github.com/google/skylark"
	"github.com/google/skylark/sk8s/kinds"
)

type Kind interface {
	skylark.HasSetField
	Underlying() interface{}
}

func IsKind(v interface{}) bool {
	_, ok := v.(Kind)
	return ok
}

var Library map[string]*skylark.Builtin = kinds.Library

func ToSky(v interface{}) (skylark.Value, error) { return kinds.ToSky(v) }
