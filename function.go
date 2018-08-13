// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

import (
	"fmt"

	"github.com/wdamron/skylark/internal/compile"
	"github.com/wdamron/skylark/syntax"
)

// A Function is a function defined by a Skylark def statement or lambda expression.
// The initialization behavior of a Skylark module is also represented by a Function.
type Function struct {
	funcode  *compile.Funcode
	defaults Tuple
	freevars Tuple

	// These fields are shared by all functions in a module.
	predeclared StringDict
	globals     []Value
	constants   []Value
}

func (fn *Function) Name() string          { return fn.funcode.Name } // "lambda" for anonymous functions
func (fn *Function) Hash() (uint32, error) { return hashString(fn.funcode.Name), nil }
func (fn *Function) Freeze()               { fn.defaults.Freeze(); fn.freevars.Freeze() }
func (fn *Function) String() string        { return toString(fn) }
func (fn *Function) Type() string          { return "function" }
func (fn *Function) Truth() Bool           { return true }

func (fn *Function) Call(thread *Thread, args Tuple, kwargs []Tuple) (Value, error) {
	if debug {
		fmt.Printf("call of %s %v %v\n", fn.Name(), args, kwargs)
	}

	// detect recursion
	for fr := thread.frame; fr != nil; fr = fr.parent {
		// We look for the same function code,
		// not function value, otherwise the user could
		// defeat the check by writing the Y combinator.
		if frfn, ok := fr.Callable().(*Function); ok && frfn.funcode == fn.funcode {
			return nil, fmt.Errorf("function %s called recursively", fn.Name())
		}
	}
	// push a new stack frame and jump to the function's entry-point
	thread.frame = &Frame{parent: thread.frame, callable: fn}
	resuming := false
	result, err := interpret(thread, args, kwargs, resuming)
	// pop the used stack frame
	thread.frame = thread.frame.parent
	return result, err
}

// Globals returns a new, unfrozen StringDict containing all global
// variables so far defined in the function's module.
func (fn *Function) Globals() StringDict {
	m := make(StringDict, len(fn.funcode.Prog.Globals))
	for i, id := range fn.funcode.Prog.Globals {
		if v := fn.globals[i]; v != nil {
			m[id.Name] = v
		}
	}
	return m
}

func (fn *Function) Position() syntax.Position { return fn.funcode.Pos }
func (fn *Function) NumParams() int            { return fn.funcode.NumParams }
func (fn *Function) Param(i int) (string, syntax.Position) {
	id := fn.funcode.Locals[i]
	return id.Name, id.Pos
}
func (fn *Function) HasVarargs() bool { return fn.funcode.HasVarargs }
func (fn *Function) HasKwargs() bool  { return fn.funcode.HasKwargs }
