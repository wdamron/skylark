// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark

import (
	"fmt"
	"io"
	"log"
	"math/big"

	"github.com/wdamron/skylark/internal/compile"
	"github.com/wdamron/skylark/resolve"
	"github.com/wdamron/skylark/syntax"
)

// A Program is a compiled Skylark program.
//
// Programs are immutable, and contain no Values.
// A Program may be created by parsing a source file (see SourceProgram)
// or by loading a previously saved compiled program (see CompiledProgram).
type Program struct {
	compiled *compile.Program
}

// CompilerVersion is the version number of the protocol for compiled
// files. Applications must not run programs compiled by one version
// with an interpreter at another version, and should thus incorporate
// the compiler version into the cache key when reusing compiled code.
const CompilerVersion = compile.Version

// NumLoads returns the number of load statements in the compiled program.
func (prog *Program) NumLoads() int { return len(prog.compiled.Loads) }

// Load(i) returns the name and position of the i'th module directly
// loaded by this one, where 0 <= i < NumLoads().
// The name is unresolved---exactly as it appears in the source.
func (prog *Program) Load(i int) (string, syntax.Position) {
	id := prog.compiled.Loads[i]
	return id.Name, id.Pos
}

// WriteTo writes the compiled module to the specified output stream.
func (prog *Program) Write(out io.Writer) error { return prog.compiled.Write(out) }

// Init creates a set of global variables for the program,
// executes the toplevel code of the specified program,
// and returns a new, unfrozen dictionary of the globals.
func (prog *Program) Init(thread *Thread, predeclared StringDict) (StringDict, error) {
	toplevel := makeToplevelFunction(prog.compiled.Toplevel, predeclared)

	_, err := toplevel.Call(thread, nil, nil)

	// Convert the global environment to a map and freeze it.
	// We return a (partial) map even in case of error.
	return toplevel.Globals(), err
}

func makeToplevelFunction(funcode *compile.Funcode, predeclared StringDict) *Function {
	// Create the Skylark value denoted by each program constant c.
	constants := make([]Value, len(funcode.Prog.Constants))
	for i, c := range funcode.Prog.Constants {
		var v Value
		switch c := c.(type) {
		case int64:
			v = MakeInt64(c)
		case *big.Int:
			v = Int{c}
		case string:
			v = String(c)
		case float64:
			v = Float(c)
		default:
			log.Fatalf("unexpected constant %T: %v", c, c)
		}
		constants[i] = v
	}

	return &Function{
		funcode:     funcode,
		predeclared: predeclared,
		globals:     make([]Value, len(funcode.Prog.Globals)),
		constants:   constants,
	}
}

// SourceProgram produces a new program by parsing, resolving,
// and compiling a Skylark source file.
// On success, it returns the parsed file and the compiled program.
// The filename and src parameters are as for syntax.Parse.
//
// The isPredeclared predicate reports whether a name is
// a pre-declared identifier of the current module.
// Its typical value is predeclared.Has,
// where predeclared is a StringDict of pre-declared values.
func SourceProgram(filename string, src interface{}, isPredeclared func(string) bool) (*syntax.File, *Program, error) {
	f, err := syntax.Parse(filename, src, 0)
	if err != nil {
		return nil, nil, err
	}

	if err := resolve.File(f, isPredeclared, Universe.Has); err != nil {
		return f, nil, err
	}

	compiled := compile.File(f.Stmts, f.Locals, f.Globals)

	return f, &Program{compiled}, nil
}

// CompiledProgram produces a new program from the representation
// of a compiled program previously saved by Program.Write.
func CompiledProgram(in io.Reader) (*Program, error) {
	prog, err := compile.ReadProgram(in)
	if err != nil {
		return nil, err
	}
	return &Program{prog}, nil
}

// A Thread contains the state of a Skylark thread,
// such as its call stack and thread-local storage.
// The Thread is threaded throughout the evaluator.
type Thread struct {
	// frame is the current Skylark execution frame.
	frame *Frame
	// suspended is non nil after a Thread is suspended.
	suspended *Frame

	// Print is the client-supplied implementation of the Skylark
	// 'print' function. If nil, fmt.Fprintln(os.Stderr, msg) is
	// used instead.
	Print func(thread *Thread, msg string)

	// Load is the client-supplied implementation of module loading.
	// Repeated calls with the same module name must return the same
	// module environment or error.
	// The error message need not include the module name.
	//
	// See example_test.go for some example implementations of Load.
	Load func(thread *Thread, module string) (StringDict, error)

	// locals holds arbitrary "thread-local" Go values belonging to the client.
	// They are accessible to the client but not to any Skylark program.
	locals map[string]interface{}
}

func (thread *Thread) Suspendable() {
	thread.suspended = thread.frame
}

func (thread *Thread) Resumable() {
	thread.frame = thread.suspended
	thread.suspended = nil
}

func (thread *Thread) PushFrame(frame *Frame) {
	frame.parent = thread.frame
	thread.frame = frame
}

func (thread *Thread) PopFrame() {
	if thread.frame == nil || thread.frame.parent == nil {
		return
	}
	thread.frame = thread.frame.parent
}

// SetLocal sets the thread-local value associated with the specified key.
// It must not be called after execution begins.
func (thread *Thread) SetLocal(key string, value interface{}) {
	if thread.locals == nil {
		thread.locals = make(map[string]interface{})
	}
	thread.locals[key] = value
}

// Local returns the thread-local value associated with the specified key.
func (thread *Thread) Local(key string) interface{} {
	return thread.locals[key]
}

// Caller returns the frame of the caller of the current function.
// It should only be used in built-ins called from Skylark code.
func (thread *Thread) Caller() *Frame { return thread.frame.parent }

// TopFrame returns the topmost stack frame.
func (thread *Thread) TopFrame() *Frame { return thread.frame }

// A Frame records a call to a Skylark function (including module toplevel)
// or a built-in function or method.
type Frame struct {
	stack     []Value
	iterstack []Iterator      // stack of active iterators
	parent    *Frame          // caller's frame (or nil)
	callable  Callable        // current function (or toplevel) or built-in
	posn      syntax.Position // source position of PC, set during error
	callpc    uint32          // PC of position of active call, set during call
	pc        uint32          // PC of return position after active call, set during call
	sp        uint32          // Stack-pointer offset of active call, set during call
}

// The Frames of a thread are structured as a spaghetti stack, not a
// slice, so that an EvalError can copy a stack efficiently and immutably.
// In hindsight using a slice would have led to a more convenient API.

func (fr *Frame) errorf(posn syntax.Position, format string, args ...interface{}) *EvalError {
	fr.posn = posn
	msg := fmt.Sprintf(format, args...)
	return &EvalError{Msg: msg, Frame: fr}
}

// Position returns the source position of the current point of execution in this frame.
func (fr *Frame) Position() syntax.Position {
	if fr.posn.IsValid() {
		return fr.posn // leaf frame only (the error)
	}
	if fn, ok := fr.callable.(*Function); ok {
		return fn.funcode.Position(fr.callpc) // position of active call
	}
	return syntax.MakePosition(&builtinFilename, 1, 0)
}

var builtinFilename = "<builtin>"

// Function returns the frame's function or built-in.
func (fr *Frame) Callable() Callable { return fr.callable }

// Parent returns the frame of the enclosing function call, if any.
func (fr *Frame) Parent() *Frame { return fr.parent }
