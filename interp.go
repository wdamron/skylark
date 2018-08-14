package skylark

// This file defines the bytecode interpreter.

import (
	"fmt"
	"os"

	"github.com/wdamron/skylark/internal/compile"
	"github.com/wdamron/skylark/syntax"
)

const vmdebug = false // TODO(adonovan): use a bitfield of specific kinds of error.

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
	stack := fr.stack[nlocals:]
	iterstack := fr.iterstack

	code, savedpc, pc, sp := fc.Code, uint32(0), uint32(0), 0
	if resuming {
		code, pc, sp = fc.Code, fr.pc, int(fr.sp)
	}

	var result Value
	var err error

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

		op := compile.Opcode(code[pc])
		pc++
		var arg uint32
		if op >= compile.OpcodeArgMin {
			// TODO(adonovan): opt: profile this.
			// Perhaps compiling big endian would be less work to decode?
			for s := uint(0); ; s += 7 {
				b := code[pc]
				pc++
				arg |= uint32(b&0x7f) << s
				if b < 0x80 {
					break
				}
			}
		}
		if vmdebug {
			fmt.Fprintln(os.Stderr, stack[:sp]) // very verbose!
			compile.PrintOp(fc, savedpc, op, arg)
		}

		switch op {
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
				break loop
			}
			stack[sp] = Bool(ok)
			sp++

		case compile.PLUS,
			compile.MINUS,
			compile.STAR,
			compile.SLASH,
			compile.SLASHSLASH,
			compile.PERCENT,
			compile.PIPE,
			compile.AMP,
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
				break loop
			}
			stack[sp] = z
			sp++

		case compile.UPLUS, compile.UMINUS:
			unop := syntax.Token(op-compile.UPLUS) + syntax.PLUS
			x := stack[sp-1]
			y, err2 := Unary(unop, x)
			if err2 != nil {
				err = err2
				break loop
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
						break loop
					}
					listExtend(xlist, yiter)
					z = xlist
				}
			}
			if z == nil {
				z, err = Binary(syntax.PLUS, x, y)
				if err != nil {
					break loop
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
			var fnkwargs Value
			if op == compile.CALL_KW || op == compile.CALL_VAR_KW {
				fnkwargs = stack[sp-1]
				sp--
			}

			var fnargs Value
			if op == compile.CALL_VAR || op == compile.CALL_VAR_KW {
				fnargs = stack[sp-1]
				sp--
			}

			// named args (pairs)
			var kvpairs []Tuple
			if nkvpairs := int(arg & 0xff); nkvpairs > 0 {
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
					break loop
				}
				items := dict.Items()
				for _, item := range items {
					if _, ok := item[0].(String); !ok {
						err = fmt.Errorf("keywords must be strings, not %s", item[0].Type())
						break loop
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
				positional = make(Tuple, npos)
				sp -= npos
				copy(positional, stack[sp:])
			}
			if fnargs != nil {
				// Add elements from *args sequence.
				iter := Iterate(fnargs)
				if iter == nil {
					err = fmt.Errorf("argument after * must be iterable, not %s", fnargs.Type())
					break loop
				}
				var elem Value
				for iter.Next(&elem) {
					positional = append(positional, elem)
				}
				iter.Done()
			}

			callable := stack[sp-1]

			if vmdebug {
				fmt.Printf("VM call %s args=%s kwargs=%s @%s\n",
					callable, positional, kvpairs, fc.Position(fr.callpc))
			}

			fr.iterstack, fr.callpc, fr.pc, fr.sp = iterstack, savedpc, pc, uint32(sp)

			if function, ok := callable.(*Function); ok {
				fr = &Frame{parent: fr, callable: function}
				// detect recursion
				for frame := fr.parent; frame != nil; frame = frame.parent {
					// We look for the same function code,
					// not function value, otherwise the user could
					// defeat the check by writing the Y combinator.
					if frfn, ok := frame.Callable().(*Function); ok && frfn.funcode == function.funcode {
						err = fmt.Errorf("function %s called recursively", function.Name())
						break loop
					}
				}

				fn = function
				fc = fn.funcode
				nlocals = len(fc.Locals)
				fr.stack = make([]Value, nlocals+fc.MaxStack)
				code, stack, locals, iterstack = fc.Code, fr.stack[nlocals:], fr.stack[:nlocals:nlocals], nil
				pc, sp = 0, 0
				if err = setArgs(locals, fn, positional, kvpairs); err != nil {
					return nil, fr.errorf(fr.Position(), "%v", err)
				}

				thread.frame = fr

				continue loop
			}

			z, err2 := Call(thread, callable, positional, kvpairs)
			if err2 != nil {
				err = err2
				break loop
			}
			if vmdebug {
				fmt.Printf("Resuming %s @ %s\n", fc.Name, fc.Position(0))
			}
			stack[sp-1] = z
			if _, ok := z.(SuspensionType); ok {
				result = z
				break loop
			}

		case compile.RETURN:
			retval := stack[sp-1]
			parent := fr.parent
			returnToCaller := false
			if parent == nil {
				returnToCaller = true
			} else if _, ok := parent.callable.(*Function); !ok {
				returnToCaller = true
			}
			thread.frame = fr
			if returnToCaller {
				if vmdebug {
					fmt.Printf("Returning from %s @ %s\n", fc.Name, fc.Position(0))
				}
				result = retval
				break loop
			}
			fr = parent
			fn = fr.callable.(*Function)
			fc = fn.funcode
			nlocals = len(fc.Locals)
			if len(fr.stack) < nlocals+fc.MaxStack {
				fr.stack = make([]Value, nlocals+fc.MaxStack)
			}
			code, stack, locals, iterstack = fc.Code, fr.stack[nlocals:], fr.stack[:nlocals:nlocals], fr.iterstack
			savedpc, pc, sp = fr.callpc, fr.pc, int(fr.sp)
			stack[sp-1] = retval
			if vmdebug {
				fmt.Printf("Resuming %s @ %s\n", fc.Name, fc.Position(0))
			}
			continue loop

		case compile.ITERPUSH:
			x := stack[sp-1]
			sp--
			iter := Iterate(x)
			if iter == nil {
				err = fmt.Errorf("%s value is not iterable", x.Type())
				break loop
			}
			iterstack = append(iterstack, iter)

		case compile.ITERJMP:
			iter := iterstack[len(iterstack)-1]
			if iter.Next(&stack[sp]) {
				sp++
			} else {
				pc = arg
			}

		case compile.ITERPOP:
			n := len(iterstack) - 1
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
				break loop
			}

		case compile.INDEX:
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2
			z, err2 := getIndex(fr, x, y)
			if err2 != nil {
				err = err2
				break loop
			}
			stack[sp] = z
			sp++

		case compile.ATTR:
			x := stack[sp-1]
			name := fc.Prog.Names[arg]
			y, err2 := getAttr(fr, x, name)
			if err2 != nil {
				err = err2
				break loop
			}
			stack[sp-1] = y

		case compile.SETFIELD:
			y := stack[sp-1]
			x := stack[sp-2]
			sp -= 2
			name := fc.Prog.Names[arg]
			if err2 := setField(fr, x, name, y); err2 != nil {
				err = err2
				break loop
			}

		case compile.MAKEDICT:
			stack[sp] = new(Dict)
			sp++

		case compile.SETDICT, compile.SETDICTUNIQ:
			dict := stack[sp-3].(*Dict)
			k := stack[sp-2]
			v := stack[sp-1]
			sp -= 3
			oldlen := dict.Len()
			if err2 := dict.Set(k, v); err2 != nil {
				err = err2
				break loop
			}
			if op == compile.SETDICTUNIQ && dict.Len() == oldlen {
				err = fmt.Errorf("duplicate key: %v", k)
				break loop
			}

		case compile.APPEND:
			elem := stack[sp-1]
			list := stack[sp-2].(*List)
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
				break loop
			}
			stack[sp] = res
			sp++

		case compile.UNPACK:
			n := int(arg)
			iterable := stack[sp-1]
			sp--
			iter := Iterate(iterable)
			if iter == nil {
				err = fmt.Errorf("got %s in sequence assignment", iterable.Type())
				break loop
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
				break loop
			}
			iter.Done()
			if i < n {
				err = fmt.Errorf("too few values to unpack (got %d, want %d)", i, n)
				break loop
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
			n := int(arg)
			tuple := make(Tuple, n)
			sp -= n
			copy(tuple, stack[sp:])
			stack[sp] = tuple
			sp++

		case compile.MAKELIST:
			n := int(arg)
			elems := make([]Value, n)
			sp -= n
			copy(elems, stack[sp:])
			stack[sp] = NewList(elems)
			sp++

		case compile.MAKEFUNC:
			funcode := fc.Prog.Functions[arg]
			freevars := stack[sp-1].(Tuple)
			defaults := stack[sp-2].(Tuple)
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
			n := int(arg)
			module := string(stack[sp-1].(String))
			sp--

			if thread.Load == nil {
				err = fmt.Errorf("load not implemented by this application")
				break loop
			}

			dict, err2 := thread.Load(thread, module)
			if err2 != nil {
				err = fmt.Errorf("cannot load %s: %v", module, err2)
				break loop
			}

			for i := 0; i < n; i++ {
				from := string(stack[sp-1-i].(String))
				v, ok := dict[from]
				if !ok {
					err = fmt.Errorf("load: name %s not found in module %s", from, module)
					break loop
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
				break loop
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
				break loop
			}
			stack[sp] = x
			sp++

		case compile.PREDECLARED:
			name := fc.Prog.Names[arg]
			x := fn.predeclared[name]
			if x == nil {
				err = fmt.Errorf("internal error: predeclared variable %s is uninitialized", name)
				break loop
			}
			stack[sp] = x
			sp++

		case compile.UNIVERSAL:
			stack[sp] = Universe[fc.Prog.Names[arg]]
			sp++

		default:
			err = fmt.Errorf("unimplemented: %s", op)
			break loop
		}
	}

	// ITERPOP the rest of the iterator stack.
	for _, iter := range iterstack {
		iter.Done()
	}

	if err != nil {
		if _, ok := err.(*EvalError); !ok {
			err = fr.errorf(fc.Position(savedpc), "%s", err.Error())
		}
	}
	return result, err
}
