// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

import (
	"fmt"

	"github.com/google/skylark/syntax"
)

// A rangeValue is a comparable, immutable, indexable sequence of integers
// defined by the three parameters to a range(...) call.
// Invariant: step != 0.
type rangeValue struct{ start, stop, step, len int }

var (
	_ Indexable  = rangeValue{}
	_ Sequence   = rangeValue{}
	_ Comparable = rangeValue{}
	_ Sliceable  = rangeValue{}
)

func (r rangeValue) Len() int          { return r.len }
func (r rangeValue) Index(i int) Value { return MakeInt(r.start + i*r.step) }
func (r rangeValue) Iterate() Iterator { return &rangeIterator{r, 0} }

func (r rangeValue) Slice(start, end, step int) Value {
	newStart := r.start + r.step*start
	newStop := r.start + r.step*end
	newStep := r.step * step
	var newLen int
	if step > 0 {
		newLen = (newStop-1-newStart)/newStep + 1
	} else {
		newLen = (newStart-1-newStop)/-newStep + 1
	}
	return rangeValue{
		start: newStart,
		stop:  newStop,
		step:  newStep,
		len:   newLen,
	}
}

func (r rangeValue) Freeze() {} // immutable
func (r rangeValue) String() string {
	if r.step != 1 {
		return fmt.Sprintf("range(%d, %d, %d)", r.start, r.stop, r.step)
	} else if r.start != 0 {
		return fmt.Sprintf("range(%d, %d)", r.start, r.stop)
	} else {
		return fmt.Sprintf("range(%d)", r.stop)
	}
}
func (r rangeValue) Type() string          { return "range" }
func (r rangeValue) Truth() Bool           { return r.len > 0 }
func (r rangeValue) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: range") }

func (x rangeValue) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(rangeValue)
	switch op {
	case syntax.EQL:
		return rangeEqual(x, y), nil
	case syntax.NEQ:
		return !rangeEqual(x, y), nil
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}

func rangeEqual(x, y rangeValue) bool {
	// Two ranges compare equal if they denote the same sequence.
	return x.len == y.len &&
		(x.len == 0 || x.start == y.start && x.step == y.step)
}

func (r rangeValue) contains(x Int) bool {
	x32, err := AsInt32(x)
	if err != nil {
		return false // out of range
	}
	delta := x32 - r.start
	quo, rem := delta/r.step, delta%r.step
	return rem == 0 && 0 <= quo && quo < r.len
}

type rangeIterator struct {
	r rangeValue
	i int
}

func (it *rangeIterator) Next(p *Value) bool {
	if it.i < it.r.len {
		*p = it.r.Index(it.i)
		it.i++
		return true
	}
	return false
}
func (*rangeIterator) Done() {}
