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

// A Set represents a Skylark set value.
type Set struct {
	ht hashtable // values are all None
}

func (s *Set) Delete(k Value) (found bool, err error) { _, found, err = s.ht.delete(k); return }
func (s *Set) Clear() error                           { return s.ht.clear() }
func (s *Set) Has(k Value) (found bool, err error)    { _, found, err = s.ht.lookup(k); return }
func (s *Set) Insert(k Value) error                   { return s.ht.insert(k, None) }
func (s *Set) Len() int                               { return int(s.ht.len) }
func (s *Set) Iterate() Iterator                      { return s.ht.iterate(s) }
func (s *Set) String() string                         { return toString(s) }
func (s *Set) Type() string                           { return "set" }
func (s *Set) elems() []Value                         { return s.ht.keys() }
func (s *Set) Freeze()                                { s.ht.freeze() }
func (s *Set) Hash() (uint32, error)                  { return 0, fmt.Errorf("unhashable type: set") }
func (s *Set) Truth() Bool                            { return s.Len() > 0 }

func (s *Set) Attr(name string) (Value, error) { return builtinAttr(s, name, setMethods) }
func (s *Set) AttrNames() []string             { return builtinAttrNames(setMethods) }

func (x *Set) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(*Set)
	switch op {
	case syntax.EQL:
		ok, err := setsEqual(x, y, depth)
		return ok, err
	case syntax.NEQ:
		ok, err := setsEqual(x, y, depth)
		return !ok, err
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}

func setsEqual(x, y *Set, depth int) (bool, error) {
	if x.Len() != y.Len() {
		return false, nil
	}
	for _, elem := range x.elems() {
		if found, _ := y.Has(elem); !found {
			return false, nil
		}
	}
	return true, nil
}

func (s *Set) Union(iter Iterator) (Value, error) {
	set := new(Set)
	for _, elem := range s.elems() {
		set.Insert(elem) // can't fail
	}
	var x Value
	for iter.Next(&x) {
		if err := set.Insert(x); err != nil {
			return nil, err
		}
	}
	return set, nil
}
