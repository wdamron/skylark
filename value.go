// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
// The following built-in Value types are known to the evaluator:
//
//      NoneType        -- NoneType
//      Bool            -- bool
//      Int             -- int
//      Float           -- float
//      String          -- string
//      *List           -- list
//      Tuple           -- tuple
//      *Dict           -- dict
//      *Set            -- set
//      *Function       -- function (implemented in Skylark)
//      *Builtin        -- builtin_function_or_method (function or method implemented in Go)
//
// Client applications may define new data types that satisfy at least
// the Value interface.  Such types may provide additional operations by
// implementing any of these optional interfaces:
//
//      Callable        -- value is callable like a function
//      Comparable      -- value defines its own comparison operations
//      Iterable        -- value is iterable using 'for' loops
//      Sequence        -- value is iterable sequence of known length
//      Indexable       -- value is sequence with efficient random access
//      HasBinary       -- value defines binary operations such as * and +
//      HasAttrs        -- value has readable fields or methods x.f
//      HasSetField     -- value has settable fields x.f
//      HasSetIndex     -- value supports element update using x[i]=y
//
// Client applications may also define domain-specific functions in Go
// and make them available to Skylark programs.  Use NewBuiltin to
// construct a built-in value that wraps a Go function.  The
// implementation of the Go function may use UnpackArgs to make sense of
// the positional and keyword arguments provided by the caller.
//
// Skylark's None value is not equal to Go's nil, but nil may be
// assigned to a Skylark Value.  Be careful to avoid allowing Go nil
// values to leak into Skylark data structures.
//
// The Compare operation requires two arguments of the same
// type, but this constraint cannot be expressed in Go's type system.
// (This is the classic "binary method problem".)
// So, each Value type's CompareSameType method is a partial function
// that compares a value only against others of the same type.
// Use the package's standalone Compare (or Equal) function to compare
// an arbitrary pair of values.
//
// To parse and evaluate a Skylark source file, use ExecFile.  The Eval
// function evaluates a single expression.  All evaluator functions
// require a Thread parameter which defines the "thread-local storage"
// of a Skylark thread and may be used to plumb application state
// through Sklyark code and into callbacks.  When evaluation fails it
// returns an EvalError from which the application may obtain a
// backtrace of active Skylark calls.
//
package skylark

import (
	"bytes"
	"fmt"
	"math"
	"reflect"

	"github.com/wdamron/skylark/syntax"
)

// Value is a value in the Skylark interpreter.
type Value interface {
	// String returns the string representation of the value.
	// Skylark string values are quoted as if by Python's repr.
	String() string

	// Type returns a short string describing the value's type.
	Type() string

	// Freeze causes the value, and all values transitively
	// reachable from it through collections and closures, to be
	// marked as frozen.  All subsequent mutations to the data
	// structure through this API will fail dynamically, making the
	// data structure immutable and safe for publishing to other
	// Skylark interpreters running concurrently.
	Freeze()

	// Truth returns the truth value of an object.
	Truth() Bool

	// Hash returns a function of x such that Equals(x, y) => Hash(x) == Hash(y).
	// Hash may fail if the value's type is not hashable, or if the value
	// contains a non-hashable value.
	Hash() (uint32, error)
}

// A Comparable is a value that defines its own equivalence relation and
// perhaps ordered comparisons.
type Comparable interface {
	Value
	// CompareSameType compares one value to another of the same Type().
	// The comparison operation must be one of EQL, NEQ, LT, LE, GT, or GE.
	// CompareSameType returns an error if an ordered comparison was
	// requested for a type that does not support it.
	//
	// Implementations that recursively compare subcomponents of
	// the value should use the CompareDepth function, not Compare, to
	// avoid infinite recursion on cyclic structures.
	//
	// The depth parameter is used to bound comparisons of cyclic
	// data structures.  Implementations should decrement depth
	// before calling CompareDepth and should return an error if depth
	// < 1.
	//
	// Client code should not call this method.  Instead, use the
	// standalone Compare or Equals functions, which are defined for
	// all pairs of operands.
	CompareSameType(op syntax.Token, y Value, depth int) (bool, error)
}

var (
	_ Comparable = None
	_ Comparable = Int{}
	_ Comparable = False
	_ Comparable = Float(0)
	_ Comparable = String("")
	_ Comparable = (*Dict)(nil)
	_ Comparable = (*List)(nil)
	_ Comparable = Tuple(nil)
	_ Comparable = (*Set)(nil)
)

// A Callable value f may be the operand of a function call, f(x).
type Callable interface {
	Value
	Name() string
	Call(thread *Thread, args Tuple, kwargs []Tuple) (Value, error)
}

var (
	_ Callable = (*Builtin)(nil)
	_ Callable = (*Function)(nil)
)

// An Iterable abstracts a sequence of values.
// An iterable value may be iterated over by a 'for' loop or used where
// any other Skylark iterable is allowed.  Unlike a Sequence, the length
// of an Iterable is not necessarily known in advance of iteration.
type Iterable interface {
	Value
	Iterate() Iterator // must be followed by call to Iterator.Done
}

// A Sequence is a sequence of values of known length.
type Sequence interface {
	Iterable
	Len() int
}

var (
	_ Sequence = (*Dict)(nil)
	_ Sequence = (*Set)(nil)
)

// An Indexable is a sequence of known length that supports efficient random access.
// It is not necessarily iterable.
type Indexable interface {
	Value
	Index(i int) Value // requires 0 <= i < Len()
	Len() int
}

// A Sliceable is a sequence that can be cut into pieces with the slice operator (x[i:j:step]).
//
// All native indexable objects are sliceable.
// This is a separate interface for backwards-compatibility.
type Sliceable interface {
	Indexable
	// For positive strides (step > 0), 0 <= start <= end <= n.
	// For negative strides (step < 0), -1 <= end <= start < n.
	// The caller must ensure that the start and end indices are valid.
	Slice(start, end, step int) Value
}

// A HasSetIndex is an Indexable value whose elements may be assigned (x[i] = y).
//
// The implementation should not add Len to a negative index as the
// evaluator does this before the call.
type HasSetIndex interface {
	Indexable
	SetIndex(index int, v Value) error
}

var (
	_ HasSetIndex = (*List)(nil)
	_ Indexable   = Tuple(nil)
	_ Indexable   = String("")
	_ Sliceable   = Tuple(nil)
	_ Sliceable   = String("")
	_ Sliceable   = (*List)(nil)
)

// An Iterator provides a sequence of values to the caller.
//
// The caller must call Done when the iterator is no longer needed.
// Operations that modify a sequence will fail if it has active iterators.
//
// Example usage:
//
// 	iter := iterable.Iterator()
//	defer iter.Done()
//	var x Value
//	for iter.Next(&x) {
//		...
//	}
//
type Iterator interface {
	// If the iterator is exhausted, Next returns false.
	// Otherwise it sets *p to the current element of the sequence,
	// advances the iterator, and returns true.
	Next(p *Value) bool
	Done()
}

// An Mapping is a mapping from keys to values, such as a dictionary.
type Mapping interface {
	Value
	// Get returns the value corresponding to the specified key,
	// or !found if the mapping does not contain the key.
	//
	// Get also defines the behavior of "v in mapping".
	// The 'in' operator reports the 'found' component, ignoring errors.
	Get(Value) (v Value, found bool, err error)
}

var _ Mapping = (*Dict)(nil)

// A HasBinary value may be used as either operand of these binary operators:
//     +   -   *   /   %   in   not in   |   &
// The Side argument indicates whether the receiver is the left or right operand.
//
// An implementation may decline to handle an operation by returning (nil, nil).
// For this reason, clients should always call the standalone Binary(op, x, y)
// function rather than calling the method directly.
type HasBinary interface {
	Value
	Binary(op syntax.Token, y Value, side Side) (Value, error)
}

type Side bool

const (
	Left  Side = false
	Right Side = true
)

// A HasAttrs value has fields or methods that may be read by a dot expression (y = x.f).
// Attribute names may be listed using the built-in 'dir' function.
//
// For implementation convenience, a result of (nil, nil) from Attr is
// interpreted as a "no such field or method" error. Implementations are
// free to return a more precise error.
type HasAttrs interface {
	Value
	Attr(name string) (Value, error) // returns (nil, nil) if attribute not present
	AttrNames() []string             // callers must not modify the result.
}

var (
	_ HasAttrs = String("")
	_ HasAttrs = new(List)
	_ HasAttrs = new(Dict)
	_ HasAttrs = new(Set)
)

// A HasSetField value has fields that may be written by a dot expression (x.f = y).
type HasSetField interface {
	HasAttrs
	SetField(name string, val Value) error
}

// toString returns the string form of value v.
// It may be more efficient than v.String() for larger values.
func toString(v Value) string {
	var buf bytes.Buffer
	path := make([]Value, 0, 4)
	writeValue(&buf, v, path)
	return buf.String()
}

// path is the list of *List and *Dict values we're currently printing.
// (These are the only potentially cyclic structures.)
func writeValue(out *bytes.Buffer, x Value, path []Value) {
	switch x := x.(type) {
	case nil:
		out.WriteString("<nil>") // indicates a bug

	case NoneType:
		out.WriteString("None")

	case Int:
		out.WriteString(x.String())

	case Bool:
		if x {
			out.WriteString("True")
		} else {
			out.WriteString("False")
		}

	case String:
		fmt.Fprintf(out, "%q", string(x))

	case *List:
		out.WriteByte('[')
		if pathContains(path, x) {
			out.WriteString("...") // list contains itself
		} else {
			for i, elem := range x.elems {
				if i > 0 {
					out.WriteString(", ")
				}
				writeValue(out, elem, append(path, x))
			}
		}
		out.WriteByte(']')

	case Tuple:
		out.WriteByte('(')
		for i, elem := range x {
			if i > 0 {
				out.WriteString(", ")
			}
			writeValue(out, elem, path)
		}
		if len(x) == 1 {
			out.WriteByte(',')
		}
		out.WriteByte(')')

	case *Function:
		fmt.Fprintf(out, "<function %s>", x.Name())

	case *Builtin:
		if x.recv != nil {
			fmt.Fprintf(out, "<built-in method %s of %s value>", x.Name(), x.recv.Type())
		} else {
			fmt.Fprintf(out, "<built-in function %s>", x.Name())
		}

	case *Dict:
		out.WriteByte('{')
		if pathContains(path, x) {
			out.WriteString("...") // dict contains itself
		} else {
			sep := ""
			for _, item := range x.Items() {
				k, v := item[0], item[1]
				out.WriteString(sep)
				writeValue(out, k, path)
				out.WriteString(": ")
				writeValue(out, v, append(path, x)) // cycle check
				sep = ", "
			}
		}
		out.WriteByte('}')

	case *Set:
		out.WriteString("set([")
		for i, elem := range x.elems() {
			if i > 0 {
				out.WriteString(", ")
			}
			writeValue(out, elem, path)
		}
		out.WriteString("])")

	default:
		out.WriteString(x.String())
	}
}

func pathContains(path []Value, x Value) bool {
	for _, y := range path {
		if x == y {
			return true
		}
	}
	return false
}

const maxdepth = 10

// Equal reports whether two Skylark values are equal.
func Equal(x, y Value) (bool, error) {
	if x, ok := x.(String); ok {
		return x == y, nil // fast path for an important special case
	}
	return EqualDepth(x, y, maxdepth)
}

// EqualDepth reports whether two Skylark values are equal.
//
// Recursive comparisons by implementations of Value.CompareSameType
// should use EqualDepth to prevent infinite recursion.
func EqualDepth(x, y Value, depth int) (bool, error) {
	return CompareDepth(syntax.EQL, x, y, depth)
}

// Compare compares two Skylark values.
// The comparison operation must be one of EQL, NEQ, LT, LE, GT, or GE.
// Compare returns an error if an ordered comparison was
// requested for a type that does not support it.
//
// Recursive comparisons by implementations of Value.CompareSameType
// should use CompareDepth to prevent infinite recursion.
func Compare(op syntax.Token, x, y Value) (bool, error) {
	return CompareDepth(op, x, y, maxdepth)
}

// CompareDepth compares two Skylark values.
// The comparison operation must be one of EQL, NEQ, LT, LE, GT, or GE.
// CompareDepth returns an error if an ordered comparison was
// requested for a pair of values that do not support it.
//
// The depth parameter limits the maximum depth of recursion
// in cyclic data structures.
func CompareDepth(op syntax.Token, x, y Value, depth int) (bool, error) {
	if depth < 1 {
		return false, fmt.Errorf("comparison exceeded maximum recursion depth")
	}
	if sameType(x, y) {
		if xcomp, ok := x.(Comparable); ok {
			return xcomp.CompareSameType(op, y, depth)
		}

		// use identity comparison
		switch op {
		case syntax.EQL:
			return x == y, nil
		case syntax.NEQ:
			return x != y, nil
		}
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}

	// different types

	// int/float ordered comparisons
	switch x := x.(type) {
	case Int:
		if y, ok := y.(Float); ok {
			if y != y {
				return false, nil // y is NaN
			}
			var cmp int
			if !math.IsInf(float64(y), 0) {
				cmp = x.rational().Cmp(y.rational()) // y is finite
			} else if y > 0 {
				cmp = -1 // y is +Inf
			} else {
				cmp = +1 // y is -Inf
			}
			return threeway(op, cmp), nil
		}
	case Float:
		if y, ok := y.(Int); ok {
			if x != x {
				return false, nil // x is NaN
			}
			var cmp int
			if !math.IsInf(float64(x), 0) {
				cmp = x.rational().Cmp(y.rational()) // x is finite
			} else if x > 0 {
				cmp = -1 // x is +Inf
			} else {
				cmp = +1 // x is -Inf
			}
			return threeway(op, cmp), nil
		}
	}

	// All other values of different types compare unequal.
	switch op {
	case syntax.EQL:
		return false, nil
	case syntax.NEQ:
		return true, nil
	}
	return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
}

func sameType(x, y Value) bool {
	return reflect.TypeOf(x) == reflect.TypeOf(y) || x.Type() == y.Type()
}

// threeway interprets a three-way comparison value cmp (-1, 0, +1)
// as a boolean comparison (e.g. x < y).
func threeway(op syntax.Token, cmp int) bool {
	switch op {
	case syntax.EQL:
		return cmp == 0
	case syntax.NEQ:
		return cmp != 0
	case syntax.LE:
		return cmp <= 0
	case syntax.LT:
		return cmp < 0
	case syntax.GE:
		return cmp >= 0
	case syntax.GT:
		return cmp > 0
	}
	panic(op)
}

// Len returns the length of a string or sequence value,
// and -1 for all others.
//
// Warning: Len(x) >= 0 does not imply Iterate(x) != nil.
// A string has a known length but is not directly iterable.
func Len(x Value) int {
	switch x := x.(type) {
	case String:
		return x.Len()
	case Sequence:
		return x.Len()
	}
	return -1
}

// Iterate return a new iterator for the value if iterable, nil otherwise.
// If the result is non-nil, the caller must call Done when finished with it.
//
// Warning: Iterate(x) != nil does not imply Len(x) >= 0.
// Some iterables may have unknown length.
func Iterate(x Value) Iterator {
	if x, ok := x.(Iterable); ok {
		return x.Iterate()
	}
	return nil
}
