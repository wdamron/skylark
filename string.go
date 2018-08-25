// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package skylark provides a Skylark interpreter.
//
// Skylark values are represented by the Value interface.
package skylark

// This file defines the data types of Skylark and their basic operations.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/skylark/syntax"
)

// String is the type of a Skylark string.
//
// A String encapsulates an an immutable sequence of bytes,
// but strings are not directly iterable. Instead, iterate
// over the result of calling one of these four methods:
// codepoints, codepoint_ords, elems, elem_ords.
type String string

func (s String) String() string        { return strconv.Quote(string(s)) }
func (s String) Type() string          { return "string" }
func (s String) Freeze()               {} // immutable
func (s String) Truth() Bool           { return len(s) > 0 }
func (s String) Hash() (uint32, error) { return hashString(string(s)), nil }
func (s String) Len() int              { return len(s) } // bytes
func (s String) Index(i int) Value     { return s[i : i+1] }

func (s String) Slice(start, end, step int) Value {
	if step == 1 {
		return String(s[start:end])
	}

	sign := signum(step)
	var str []byte
	for i := start; signum(end-i) == sign; i += step {
		str = append(str, s[i])
	}
	return String(str)
}

func (s String) Attr(name string) (Value, error) { return builtinAttr(s, name, stringMethods) }
func (s String) AttrNames() []string             { return builtinAttrNames(stringMethods) }

func (x String) CompareSameType(op syntax.Token, y_ Value, depth int) (bool, error) {
	y := y_.(String)
	return threeway(op, strings.Compare(string(x), string(y))), nil
}

func AsString(x Value) (string, bool) { v, ok := x.(String); return string(v), ok }

// A stringIterable is an iterable whose iterator yields a sequence of
// either Unicode code points or elements (bytes),
// either numerically or as successive substrings.
type stringIterable struct {
	s          String
	ords       bool
	codepoints bool
}

var _ Iterable = (*stringIterable)(nil)

func (si stringIterable) String() string {
	var etype string
	if si.codepoints {
		etype = "codepoint"
	} else {
		etype = "elem"
	}
	if si.ords {
		return si.s.String() + "." + etype + "_ords()"
	} else {
		return si.s.String() + "." + etype + "s()"
	}
}
func (si stringIterable) Type() string {
	if si.codepoints {
		return "codepoints"
	} else {
		return "elems"
	}
}
func (si stringIterable) Freeze()               {} // immutable
func (si stringIterable) Truth() Bool           { return True }
func (si stringIterable) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", si.Type()) }
func (si stringIterable) Iterate() Iterator     { return &stringIterator{si, 0} }

type stringIterator struct {
	si stringIterable
	i  int
}

func (it *stringIterator) Next(p *Value) bool {
	s := it.si.s[it.i:]
	if s == "" {
		return false
	}
	if it.si.codepoints {
		r, sz := utf8.DecodeRuneInString(string(s))
		if !it.si.ords {
			*p = s[:sz]
		} else {
			*p = MakeInt(int(r))
		}
		it.i += sz
	} else {
		b := int(s[0])
		if !it.si.ords {
			*p = s[:1]
		} else {
			*p = MakeInt(b)
		}
		it.i += 1
	}
	return true
}

func (*stringIterator) Done() {}
