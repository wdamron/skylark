package skylark

// This file defines the bytecode interpreter.

import (
	"fmt"
	"os"

	"github.com/google/skylark/internal/compile"
	"github.com/google/skylark/syntax"
)

const (
	vmdebug       = false // TODO(adonovan): use a bitfield of specific kinds of error.
	checkstackops = true
)

func (fn *Function) Call(thread *Thread, args Tuple, kwargs []Tuple) (Value, error) {
	if debug {
		fmt.Printf("call of %s %v %v\n", fn.Name(), args, kwargs)
	}

	if fn.isRecursive(thread.frame) {
		return nil, fmt.Errorf("function %s called recursively", fn.Name())
	}
	// push a new stack frame and jump to the function's entry-point
	thread.frame = &Frame{parent: thread.frame, callable: fn}
	resuming := false
	result, err := interpret(thread, args, kwargs, resuming)
	// pop the used stack frame
	thread.frame = thread.frame.parent
	return result, err
}

func (fn *Function) isRecursive(frame *Frame) bool {
	for fr := frame; fr != nil; fr = fr.parent {
		// We look for the same function code,
		// not function value, otherwise the user could
		// defeat the check by writing the Y combinator.
		if frfn, ok := fr.Callable().(*Function); ok && frfn.funcode == fn.funcode {
			return true
		}
	}
	return false
}

// frameStack contains values for a frame's stack, addressable by offsets from the stack-pointer (sp).
type frameStack []Value

func (stack frameStack) check(op compile.Opcode, sp, pops, pushes int) error {
	if !checkstackops {
		return nil
	}
	size := len(stack)
	if pops > 0 {
		hi, lo := sp-1, sp-pops
		if hi < 0 || hi > size-1 || lo < 0 || lo > size-1 {
			return fmt.Errorf("internal error: cannot pop %v values from stack of size %v from position %v for op %s", pops, size, sp-1, op.String())
		}
	}
	sp -= pops
	if pushes > pops {
		hi, lo := sp+pushes-1, sp
		if hi < 0 || hi > size-1 || lo < 0 || lo > size-1 {
			return fmt.Errorf("internal error: cannot push %v values onto stack of size %v at position %v for op %s", pops, size, sp, op.String())
		}
	}
	return nil
}

type exceptionHandler struct {
	pc, sp uint32
}

func getExceptionType(err error) string {
	// TODO(wdamron): add builtin error types
	if err == nil {
		return "None"
	}
	return "Exception"
}

// guardsp returns sp as an offset from the saved sp of the inner-most enclosing
// exception handler, or returns sp if no exception handlers are active.
func guardsp(sp int, exhandlers []exceptionHandler) int {
	if !checkstackops {
		return 0
	}
	if len(exhandlers) > 0 {
		return sp - int(exhandlers[len(exhandlers)-1].sp)
	}
	return sp
}

// TODO(adonovan):
// - optimize position table.
// - opt: reduce allocations by preallocating a large stack, saving it
//   in the thread, and slicing it.
// - opt: record MaxIterStack during compilation and preallocate the stack.

func interpret(thread *Thread, args Tuple, kwargs []Tuple, resuming bool) (Value, error) {
	fr := thread.frame
	fn := fr.callable.(*Function)
	fc := fn.funcode

	nlocals := len(fc.Locals)
	if !resuming {
		fr.stack = make([]Value, nlocals+fc.MaxStack)
	} else if len(fr.stack) < nlocals+fc.MaxStack {
		existing := fr.stack
		fr.stack = make([]Value, nlocals+fc.MaxStack)
		copy(fr.stack, existing)
	}

	locals := fr.stack[:nlocals:nlocals] // local variables, starting with parameters
	stack := frameStack(fr.stack[nlocals:])
	iterstack := fr.iterstack
	exhandlers := fr.exhandlers

	code, savedpc, pc, sp := fc.Code, uint32(0), uint32(0), 0
	if resuming {
		code, pc, sp = fc.Code, fr.pc, int(fr.sp)
	}

	var result Value
	var err error
	var savedErr error

	if !resuming {
		err = setArgs(locals, fn, args, kwargs)
		if err != nil {
			return nil, fr.errorf(fr.Position(), "%v", err)
		}
	}

	if vmdebug {
		fmt.Printf("Entering %s @ %s\n", fc.Name, fc.Position(0))
		fmt.Printf("%d stack, %d locals\n", len(stack), len(locals))
		defer fmt.Println("Leaving ", fc.Name)
	}

	// TODO(adonovan): add static check that beneath this point
	// - there is exactly one return statement
	// - there is no redefinition of 'err'.

loop:
	for {
		savedpc = pc

		if err != nil {
			if len(exhandlers) == 0 {
				break loop
			}
			handler := exhandlers[len(exhandlers)-1]
			pc, sp = handler.pc, int(handler.sp)
			savedErr = err
			err = nil
		}
		var op compile.Opcode
		var arg uint32
		op, arg, pc = compile.DecodeOpUnsafe(code, pc)
		if checkstackops && !compile.IsVariableStackEffect(op) {
			stackPops, stackPushes := compile.StackEffect(op)
			if stackErr := stack.check(op, guardsp(sp, exhandlers), stackPops, stackPushes); stackErr != nil {
				err = stackErr
				break loop
			}
		}
		if vmdebug {
			fmt.Fprintln(os.Stderr, stack[:sp]) // very verbose!
			compile.PrintOp(fc, savedpc, op, arg)
		}

		switch op {

		case compile.ERROR:
			if len(exhandlers) <= 0 {
				err = fmt.Errorf("internal error: empty exception handler stack during %s", op.String())
				break loop
			}
			exhandlers = exhandlers[:len(exhandlers)-1]
			x := stack[sp-1]
			expectedExType, ok := x.(String)
			if !ok {
				xt := "<nil>"
				if x != nil {
					xt = x.Type()
				}
				err = fmt.Errorf("expected exception type must resolve to a string, found %s", xt)
				savedErr = nil
				continue loop
			}
			if getExceptionType(savedErr) != string(expectedExType) {
				err = savedErr
				savedErr = nil
				continue loop
			}
			if savedErr != nil {
				stack[sp-1] = String(savedErr.Error())
				savedErr = nil
			} else {
				stack[sp-1] = None
			}

		case compile.EXCEPTPUSH:
			exhandlers = append(exhandlers, exceptionHandler{pc: arg, sp: uint32(sp)})

		case compile.EXCEPTPOP:
			if len(exhandlers) <= 0 {
				err = fmt.Errorf("internal error: empty exception handler stack during %s", op.String())
				break loop
			}
			exhandlers = exhandlers[:len(exhandlers)-1]

		case compile.NOP:
			// nop

		case compile.DUP:
			stack[sp] = stack[sp-1]
			sp++

		case compile.DUP2:
			stack[sp] = stack[sp-2]
			stack[sp+1] = stack[sp-1]
			sp += 2

		case compile.POP:
			sp--

		case compile.EXCH:
			stack[sp-2], stack[sp-1] = stack[sp-1], stack[sp-2]

		case compile.EQL, compile.NEQ, compile.GT, compile.LT, compile.LE, compile.GE:
			op := syntax.Token(op-compile.EQL) + syntax.EQL
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2
			ok, err2 := Compare(op, x, y)
			if err2 != nil {
				err = err2
				continue loop
			}
			stack[sp] = Bool(ok)
			sp++

		case compile.PLUS,
			compile.MINUS,
			compile.STAR,
			compile.SLASH,
			compile.SLASHSLASH,
			compile.PERCENT,
			compile.AMP,
			compile.PIPE,
			compile.CIRCUMFLEX,
			compile.LTLT,
			compile.GTGT,
			compile.IN:
			binop := syntax.Token(op-compile.PLUS) + syntax.PLUS
			if op == compile.IN {
				binop = syntax.IN // IN token is out of order
			}
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2
			z, err2 := Binary(binop, x, y)
			if err2 != nil {
				err = err2
				continue loop
			}
			stack[sp] = z
			sp++

		case compile.UPLUS, compile.UMINUS, compile.TILDE:
			var unop syntax.Token
			if op == compile.TILDE {
				unop = syntax.TILDE
			} else {
				unop = syntax.Token(op-compile.UPLUS) + syntax.PLUS
			}
			x := stack[sp-1]
			y, err2 := Unary(unop, x)
			if err2 != nil {
				err = err2
				continue loop
			}
			stack[sp-1] = y

		case compile.INPLACE_ADD:
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2

			// It's possible that y is not Iterable but
			// nonetheless defines x+y, in which case we
			// should fall back to the general case.
			var z Value
			if xlist, ok := x.(*List); ok {
				if yiter, ok := y.(Iterable); ok {
					if err = xlist.checkMutable("apply += to", true); err != nil {
						continue loop
					}
					listExtend(xlist, yiter)
					z = xlist
				}
			}
			if z == nil {
				z, err = Binary(syntax.PLUS, x, y)
				if err != nil {
					continue loop
				}
			}

			stack[sp] = z
			sp++

		case compile.NONE:
			stack[sp] = None
			sp++

		case compile.TRUE:
			stack[sp] = True
			sp++

		case compile.FALSE:
			stack[sp] = False
			sp++

		case compile.JMP:
			pc = arg

		case compile.CALL, compile.CALL_VAR, compile.CALL_KW, compile.CALL_VAR_KW:
			// VARIABLE STACK EFFECT
			var fnkwargs Value
			if op == compile.CALL_KW || op == compile.CALL_VAR_KW {
				if err = stack.check(op, guardsp(sp, exhandlers), 1, 0); err != nil {
					continue loop
				}
				fnkwargs = stack[sp-1]
				sp--
			}

			var fnargs Value
			if op == compile.CALL_VAR || op == compile.CALL_VAR_KW {
				if err = stack.check(op, guardsp(sp, exhandlers), 1, 0); err != nil {
					continue loop
				}
				fnargs = stack[sp-1]
				sp--
			}

			// named args (pairs)
			var kvpairs []Tuple
			if nkvpairs := int(arg & 0xff); nkvpairs > 0 {
				if err = stack.check(op, guardsp(sp, exhandlers), 2*nkvpairs, 0); err != nil {
					continue loop
				}
				kvpairs = make([]Tuple, 0, nkvpairs)
				kvpairsAlloc := make(Tuple, 2*nkvpairs) // allocate a single backing array
				sp -= 2 * nkvpairs
				for i := 0; i < nkvpairs; i++ {
					pair := kvpairsAlloc[:2:2]
					kvpairsAlloc = kvpairsAlloc[2:]
					pair[0] = stack[sp+2*i]   // name
					pair[1] = stack[sp+2*i+1] // value
					kvpairs = append(kvpairs, pair)
				}
			}
			if fnkwargs != nil {
				// Add key/value items from **kwargs dictionary.
				dict, ok := fnkwargs.(*Dict)
				if !ok {
					err = fmt.Errorf("argument after ** must be a mapping, not %s", fnkwargs.Type())
					continue loop
				}
				items := dict.Items()
				for _, item := range items {
					if _, ok := item[0].(String); !ok {
						err = fmt.Errorf("keywords must be strings, not %s", item[0].Type())
						continue loop
					}
				}
				if len(kvpairs) == 0 {
					kvpairs = items
				} else {
					kvpairs = append(kvpairs, items...)
				}
			}

			// positional args
			var positional Tuple
			if npos := int(arg >> 8); npos > 0 {
				if err = stack.check(op, guardsp(sp, exhandlers), npos, 0); err != nil {
					continue loop
				}
				positional = make(Tuple, npos)
				sp -= npos
				copy(positional, stack[sp:])
			}
			if fnargs != nil {
				// Add elements from *args sequence.
				iter := Iterate(fnargs)
				if iter == nil {
					err = fmt.Errorf("argument after * must be iterable, not %s", fnargs.Type())
					continue loop
				}
				var elem Value
				for iter.Next(&elem) {
					positional = append(positional, elem)
				}
				iter.Done()
			}

			if err = stack.check(op, guardsp(sp, exhandlers), 1, 1); err != nil {
				continue loop
			}

			callable := stack[sp-1]

			if vmdebug {
				fmt.Printf("VM call %s args=%s kwargs=%s @%s\n",
					callable, positional, kvpairs, fc.Position(fr.callpc))
			}

			fr.iterstack, fr.exhandlers, fr.callpc, fr.pc, fr.sp = iterstack, exhandlers, savedpc, pc, uint32(sp)

			// If the callable is a compiled function, jump directly to its entry-point:
			if function, ok := callable.(*Function); ok {
				if function.isRecursive(fr) {
					err = fmt.Errorf("function %s called recursively", function.Name())
					continue loop
				}
				fr = &Frame{parent: fr, callable: function}
				fn = function
				fc = fn.funcode
				nlocals = len(fc.Locals)
				fr.stack = make([]Value, nlocals+fc.MaxStack)
				code, stack, locals, iterstack, exhandlers = fc.Code, frameStack(fr.stack[nlocals:]), fr.stack[:nlocals:nlocals], nil, nil
				pc, sp = 0, 0
				if err = setArgs(locals, fn, positional, kvpairs); err != nil {
					return nil, fr.errorf(fr.Position(), "%v", err)
				}

				thread.frame = fr

				continue loop
			}

			// If the callable is a non-compiled (builtin) functions/method, call it directly:
			z, err2 := Call(thread, callable, positional, kvpairs)
			if err2 != nil {
				err = err2
				continue loop
			}
			if vmdebug {
				fmt.Printf("Resuming %s @ %s\n", fc.Name, fc.Position(0))
			}
			stack[sp-1] = z
			if thread.SuspendedFrame() != nil {
				result = z
				break loop
			}

		case compile.RETURN:
			retval := stack[sp-1]
			parent := fr.parent
			returnToCaller := false
			if parent == nil || thread.SuspendedFrame() != nil {
				returnToCaller = true
			}
			var parentFn *Function
			if parent != nil {
				var isFunction bool
				parentFn, isFunction = parent.callable.(*Function)
				if !isFunction {
					returnToCaller = true
				}
			}
			thread.frame = fr
			// If the caller is not a compiled function, return to it directly:
			if returnToCaller {
				if vmdebug {
					fmt.Printf("Returning from %s @ %s\n", fc.Name, fc.Position(0))
				}
				result = retval
				break loop
			}
			// If the caller is a compiled function, jump to its frame's next instruction (PC):
			fr = parent
			fn = parentFn
			fc = fn.funcode
			nlocals = len(fc.Locals)
			if len(fr.stack) < nlocals+fc.MaxStack {
				stack = make([]Value, nlocals+fc.MaxStack)
				copy(stack, fr.stack)
				fr.stack = stack
			}
			code, pc, sp = fc.Code, fr.pc, int(fr.sp)
			stack, locals, iterstack, exhandlers = frameStack(fr.stack[nlocals:]), fr.stack[:nlocals:nlocals], fr.iterstack, fr.exhandlers
			stack[sp-1] = retval
			if vmdebug {
				fmt.Printf("Resuming %s @ %s\n", fc.Name, fc.Position(0))
			}

		case compile.ITERPUSH:
			x := stack[sp-1]
			sp--
			iter := Iterate(x)
			if iter == nil {
				err = fmt.Errorf("%s value is not iterable", x.Type())
				continue loop
			}
			iterstack = append(iterstack, iter)

		case compile.ITERJMP:
			// VARIABLE STACK EFFECT
			if err = stack.check(op, guardsp(sp, exhandlers), 0, 1); err != nil {
				continue loop
			}
			if len(iterstack) == 0 {
				err = fmt.Errorf("iterator stack is empty")
				continue loop
			}
			iter := iterstack[len(iterstack)-1]
			if iter.Next(&stack[sp]) {
				sp++
			} else {
				pc = arg
			}

		case compile.ITERPOP:
			n := len(iterstack) - 1
			if n < 0 {
				err = fmt.Errorf("iterator stack is empty")
				continue loop
			}
			iterstack[n].Done()
			iterstack = iterstack[:n]

		case compile.NOT:
			stack[sp-1] = !stack[sp-1].Truth()

		case compile.SETINDEX:
			z := stack[sp-1]
			y := stack[sp-2]
			x := stack[sp-3]
			sp -= 3
			err = setIndex(fr, x, y, z)
			if err != nil {
				continue loop
			}

		case compile.INDEX:
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2
			z, err2 := getIndex(fr, x, y)
			if err2 != nil {
				err = err2
				continue loop
			}
			stack[sp] = z
			sp++

		case compile.ATTR:
			x := stack[sp-1]
			name := fc.Prog.Names[arg]
			y, err2 := getAttr(fr, x, name)
			if err2 != nil {
				err = err2
				continue loop
			}
			stack[sp-1] = y

		case compile.SETFIELD:
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2
			name := fc.Prog.Names[arg]
			if err2 := setField(fr, x, name, y); err2 != nil {
				err = err2
				continue loop
			}

		case compile.MAKEDICT:
			stack[sp] = new(Dict)
			sp++

		case compile.SETDICT, compile.SETDICTUNIQ:
			dict, ok := stack[sp-3].(*Dict)
			if !ok {
				argType := "<nil>"
				if stack[sp-3] != nil {
					argType = stack[sp-3].Type()
				}
				err = fmt.Errorf("argument to %s must be a dict, not %s", op.String(), argType)
				continue loop
			}
			k := stack[sp-2]
			v := stack[sp-1]
			sp -= 3
			oldlen := dict.Len()
			if err2 := dict.Set(k, v); err2 != nil {
				err = err2
				continue loop
			}
			if op == compile.SETDICTUNIQ && dict.Len() == oldlen {
				err = fmt.Errorf("duplicate key: %v", k)
				continue loop
			}

		case compile.APPEND:
			elem := stack[sp-1]
			list, ok := stack[sp-2].(*List)
			if !ok {
				argType := "<nil>"
				if stack[sp-2] != nil {
					argType = stack[sp-2].Type()
				}
				err = fmt.Errorf("argument to %s must be a list, not %s", op.String(), argType)
				continue loop
			}
			sp -= 2
			list.elems = append(list.elems, elem)

		case compile.SLICE:
			x := stack[sp-4]
			lo := stack[sp-3]
			hi := stack[sp-2]
			step := stack[sp-1]
			sp -= 4
			res, err2 := slice(x, lo, hi, step)
			if err2 != nil {
				err = err2
				continue loop
			}
			stack[sp] = res
			sp++

		case compile.UNPACK:
			// VARIABLE STACK EFFECT
			n := int(arg)
			if err = stack.check(op, guardsp(sp, exhandlers), 1, n); err != nil {
				continue loop
			}
			iterable := stack[sp-1]
			sp--
			iter := Iterate(iterable)
			if iter == nil {
				err = fmt.Errorf("got %s in sequence assignment", iterable.Type())
				continue loop
			}
			i := 0
			sp += n
			for i < n && iter.Next(&stack[sp-1-i]) {
				i++
			}
			var dummy Value
			if iter.Next(&dummy) {
				// NB: Len may return -1 here in obscure cases.
				err = fmt.Errorf("too many values to unpack (got %d, want %d)", Len(iterable), n)
				continue loop
			}
			iter.Done()
			if i < n {
				err = fmt.Errorf("too few values to unpack (got %d, want %d)", i, n)
				continue loop
			}

		case compile.CJMP:
			if stack[sp-1].Truth() {
				pc = arg
			}
			sp--

		case compile.CONSTANT:
			stack[sp] = fn.constants[arg]
			sp++

		case compile.MAKETUPLE:
			// VARIABLE STACK EFFECT
			n := int(arg)
			if err = stack.check(op, guardsp(sp, exhandlers), n, 1); err != nil {
				continue loop
			}
			tuple := make(Tuple, n)
			sp -= n
			copy(tuple, stack[sp:])
			stack[sp] = tuple
			sp++

		case compile.MAKELIST:
			// VARIABLE STACK EFFECT
			n := int(arg)
			if err = stack.check(op, guardsp(sp, exhandlers), n, 1); err != nil {
				continue loop
			}
			elems := make([]Value, n)
			sp -= n
			copy(elems, stack[sp:])
			stack[sp] = NewList(elems)
			sp++

		case compile.MAKEFUNC:
			funcode := fc.Prog.Functions[arg]
			var ok bool
			var freevars Tuple
			freevars, ok = stack[sp-1].(Tuple)
			if !ok {
				argType := "<nil>"
				if stack[sp-1] != nil {
					argType = stack[sp-1].Type()
				}
				err = fmt.Errorf("freevars argument to %s must be a tuple, not %s", op.String(), argType)
				continue loop
			}
			var defaults Tuple
			defaults, ok = stack[sp-2].(Tuple)
			if !ok {
				argType := "<nil>"
				if stack[sp-2] != nil {
					argType = stack[sp-2].Type()
				}
				err = fmt.Errorf("defaults argument to %s must be a tuple, not %s", op.String(), argType)
				continue loop
			}
			sp -= 2
			stack[sp] = &Function{
				funcode:     funcode,
				predeclared: fn.predeclared,
				globals:     fn.globals,
				constants:   fn.constants,
				defaults:    defaults,
				freevars:    freevars,
			}
			sp++

		case compile.LOAD:
			// VARIABLE STACK EFFECT (for stack checking), but the net result is 1 pop
			n := int(arg)
			if err = stack.check(op, guardsp(sp, exhandlers), 1+n, n); err != nil {
				continue loop
			}
			moduleString, ok := stack[sp-1].(String)
			if !ok {
				argType := "<nil>"
				if stack[sp-1] != nil {
					argType = stack[sp-1].Type()
				}
				err = fmt.Errorf("argument to %s must be a string, not %s", op.String(), argType)
				continue loop
			}
			module := string(moduleString)
			sp--

			if thread.Load == nil {
				err = fmt.Errorf("load not implemented by this application")
				continue loop
			}

			dict, err2 := thread.Load(thread, module)
			if err2 != nil {
				err = fmt.Errorf("cannot load %s: %v", module, err2)
				continue loop
			}

			for i := 0; i < n; i++ {
				name, ok := stack[sp-1-i].(String)
				if !ok {
					argType := "<nil>"
					if stack[sp-1] != nil {
						argType = stack[sp-1-i].Type()
					}
					err = fmt.Errorf("load: value name in module %s must be a string, not %s", module, argType)
					continue loop
				}
				from := string(name)
				var v Value
				v, ok = dict[from]
				if !ok {
					err = fmt.Errorf("load: name %s not found in module %s", from, module)
					continue loop
				}
				stack[sp-1-i] = v
			}

		case compile.SETLOCAL:
			locals[arg] = stack[sp-1]
			sp--

		case compile.SETGLOBAL:
			fn.globals[arg] = stack[sp-1]
			sp--

		case compile.LOCAL:
			x := locals[arg]
			if x == nil {
				err = fmt.Errorf("local variable %s referenced before assignment", fc.Locals[arg].Name)
				continue loop
			}
			stack[sp] = x
			sp++

		case compile.FREE:
			stack[sp] = fn.freevars[arg]
			sp++

		case compile.GLOBAL:
			x := fn.globals[arg]
			if x == nil {
				err = fmt.Errorf("global variable %s referenced before assignment", fc.Prog.Globals[arg].Name)
				continue loop
			}
			stack[sp] = x
			sp++

		case compile.PREDECLARED:
			name := fc.Prog.Names[arg]
			x := fn.predeclared[name]
			if x == nil {
				err = fmt.Errorf("internal error: predeclared variable %s is uninitialized", name)
				continue loop
			}
			stack[sp] = x
			sp++

		case compile.UNIVERSAL:
			stack[sp] = Universe[fc.Prog.Names[arg]]
			sp++

		default:
			err = fmt.Errorf("unimplemented: %s", op)
			continue loop
		}
	}

	// ITERPOP the rest of the iterator stack.
	for _, iter := range iterstack {
		iter.Done()
	}
	if result == nil {
		result = None
	}
	if err != nil {
		if _, ok := err.(*EvalError); !ok {
			err = fr.errorf(fc.Position(savedpc), "%s", err.Error())
		}
	}
	return result, err
}
