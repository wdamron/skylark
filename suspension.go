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

// SuspensionType is the type of Suspension.  Its only legal value is Suspension.
// (We represent it as a number, not struct{}, so that Suspension may be constant.)
type SuspensionType byte

const Suspended = SuspensionType(1)

func (SuspensionType) String() string        { return "Suspension" }
func (SuspensionType) Type() string          { return "SuspensionType" }
func (SuspensionType) Freeze()               {} // immutable
func (SuspensionType) Truth() Bool           { return False }
func (SuspensionType) Hash() (uint32, error) { return 0, nil }
func (SuspensionType) CompareSameType(op syntax.Token, y Value, depth int) (bool, error) {
	return threeway(op, 0), nil
}
