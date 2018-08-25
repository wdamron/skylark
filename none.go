// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

import (
	"github.com/google/skylark/syntax"
)

// NoneType is the type of None.  Its only legal value is None.
// (We represent it as a number, not struct{}, so that None may be constant.)
type NoneType byte

const None = NoneType(0)

func (NoneType) String() string        { return "None" }
func (NoneType) Type() string          { return "NoneType" }
func (NoneType) Freeze()               {} // immutable
func (NoneType) Truth() Bool           { return False }
func (NoneType) Hash() (uint32, error) { return 0, nil }
func (NoneType) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	return threeway(op, 0), nil
}
