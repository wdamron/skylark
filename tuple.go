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

// A Tuple represents a Skylark tuple value.
type Tuple []Value

func (t Tuple) Len() int          { return len(t) }
func (t Tuple) Index(i int) Value { return t[i] }

func (t Tuple) Slice(start, end, step int) Value {
	if step == 1 {
		return t[start:end]
	}

	sign := signum(step)
	var tuple Tuple
	for i := start; signum(end-i) == sign; i += step {
		tuple = append(tuple, t[i])
	}
	return tuple
}

func (t Tuple) Iterate() Iterator { return &tupleIterator{elems: t} }
func (t Tuple) Freeze() {
	for _, elem := range t {
		elem.Freeze()
	}
}
func (t Tuple) String() string { return toString(t) }
func (t Tuple) Type() string   { return "tuple" }
func (t Tuple) Truth() Bool    { return len(t) > 0 }

func (x Tuple) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(Tuple)
	return sliceCompare(op, x, y, depth)
}

func (t Tuple) Hash() (uint32, error) {
	// Use same algorithm as Python.
	var x, mult uint32 = 0x345678, 1000003
	for _, elem := range t {
		y, err := elem.Hash()
		if err != nil {
			return 0, err
		}
		x = x ^ y*mult
		mult += 82520 + uint32(len(t)+len(t))
	}
	return x, nil
}

type tupleIterator struct{ elems Tuple }

func (it *tupleIterator) Next(p *Value) bool {
	if len(it.elems) > 0 {
		*p = it.elems[0]
		it.elems = it.elems[1:]
		return true
	}
	return false
}

func (it *tupleIterator) Done() {}
