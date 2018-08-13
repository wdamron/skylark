// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

import (
	"github.com/wdamron/skylark/syntax"
)

// Bool is the type of a Skylark bool.
type Bool bool

const (
	False Bool = false
	True  Bool = true
)

func (b Bool) String() string {
	if b {
		return "True"
	} else {
		return "False"
	}
}
func (b Bool) Type() string          { return "bool" }
func (b Bool) Freeze()               {} // immutable
func (b Bool) Truth() Bool           { return b }
func (b Bool) Hash() (uint32, error) { return uint32(b2i(bool(b))), nil }
func (x Bool) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(Bool)
	return threeway(op, b2i(bool(x))-b2i(bool(y))), nil
}

func b2i(b bool) int {
	if b {
		return 1
	} else {
		return 0
	}
}
