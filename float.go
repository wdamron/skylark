// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

import (
	"math"
	"math/big"
	"strconv"

	"github.com/wdamron/skylark/syntax"
)

// Float is the type of a Skylark float.
type Float float64

func (f Float) String() string { return strconv.FormatFloat(float64(f), 'g', 6, 64) }
func (f Float) Type() string   { return "float" }
func (f Float) Freeze()        {} // immutable
func (f Float) Truth() Bool    { return f != 0.0 }
func (f Float) Hash() (uint32, error) {
	// Equal float and int values must yield the same hash.
	// TODO(adonovan): opt: if f is non-integral, and thus not equal
	// to any Int, we can avoid the Int conversion and use a cheaper hash.
	if isFinite(float64(f)) {
		return finiteFloatToInt(f).Hash()
	}
	return 1618033, nil // NaN, +/-Inf
}

func floor(f Float) Float { return Float(math.Floor(float64(f))) }

// isFinite reports whether f represents a finite rational value.
// It is equivalent to !math.IsNan(f) && !math.IsInf(f, 0).
func isFinite(f float64) bool {
	return math.Abs(f) <= math.MaxFloat64
}

func (x Float) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(Float)
	switch op {
	case syntax.EQL:
		return x == y, nil
	case syntax.NEQ:
		return x != y, nil
	case syntax.LE:
		return x <= y, nil
	case syntax.LT:
		return x < y, nil
	case syntax.GE:
		return x >= y, nil
	case syntax.GT:
		return x > y, nil
	}
	panic(op)
}

func (f Float) rational() *big.Rat { return new(big.Rat).SetFloat64(float64(f)) }

// AsFloat returns the float64 value closest to x.
// The f result is undefined if x is not a float or int.
func AsFloat(x Value) (f float64, ok bool) {
	switch x := x.(type) {
	case Float:
		return float64(x), true
	case Int:
		return float64(x.Float()), true
	}
	return 0, false
}

func (x Float) Mod(y Float) Float { return Float(math.Mod(float64(x), float64(y))) }
