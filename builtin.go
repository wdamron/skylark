// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

// A Builtin is a function implemented in Go.
type Builtin struct {
	name string
	fn   func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error)
	recv Value // for bound methods (e.g. "".startswith)
}

func (b *Builtin) Name() string { return b.name }
func (b *Builtin) Freeze() {
	if b.recv != nil {
		b.recv.Freeze()
	}
}
func (b *Builtin) Hash() (uint32, error) {
	h := hashString(b.name)
	if b.recv != nil {
		h ^= 5521
	}
	return h, nil
}
func (b *Builtin) Receiver() Value { return b.recv }
func (b *Builtin) String() string  { return toString(b) }
func (b *Builtin) Type() string    { return "builtin_function_or_method" }
func (b *Builtin) Call(thread *Thread, args Tuple, kwargs []Tuple) (Value, error) {
	thread.frame = &Frame{parent: thread.frame, callable: b}
	result, err := b.fn(thread, b, args, kwargs)
	thread.frame = thread.frame.parent
	return result, err
}
func (b *Builtin) Truth() Bool { return true }

// NewBuiltin returns a new 'builtin_function_or_method' value with the specified name
// and implementation.  It compares unequal with all other values.
func NewBuiltin(name string, fn func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error)) *Builtin {
	return &Builtin{name: name, fn: fn}
}

// BindReceiver returns a new Builtin value representing a method
// closure, that is, a built-in function bound to a receiver value.
//
// In the example below, the value of f is the string.index
// built-in method bound to the receiver value "abc":
//
//     f = "abc".index; f("a"); f("b")
//
// In the common case, the receiver is bound only during the call,
// but this still results in the creation of a temporary method closure:
//
//     "abc".index("a")
//
func (b *Builtin) BindReceiver(recv Value) *Builtin {
	return &Builtin{name: b.name, fn: b.fn, recv: recv}
}
