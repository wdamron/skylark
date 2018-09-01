// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark_test

import (
	"fmt"
	"testing"

	. "github.com/google/skylark"
	"github.com/google/skylark/resolve"
	"github.com/google/skylark/skylarkstruct"
	"github.com/google/skylark/skylarktest"
)

func init() {
	resolve.AllowTryExcept = true
}

func TestSuspendResume(t *testing.T) {
	filename := "suspend.sky"

	predeclared := StringDict{
		"struct_val": skylarkstruct.FromStringDict(skylarkstruct.Default, StringDict{
			"a": String("a"),
			"b": String("b"),
			"c": String("c"),
		}),
		"long_running_builtin": NewBuiltin("long_running_builtin",
			func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
				thread.Suspendable(args, kwargs)
				return None, nil
			}),
		"Exception":  BaseException,
		"TypeError":  NewTypeError(fmt.Errorf("some type error")),
		"ValueError": NewValueError(fmt.Errorf("some value error")),
		"log_error": NewBuiltin("log_error",
			func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
				if len(args) == 2 {
					t.Logf("i=%s err: %s", args[0].String(), args[1].String())
				}
				return None, nil
			}),
		"raise_value_error": NewBuiltin("raise_value_error",
			func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
				var arg Value = String("")
				if len(args) > 0 {
					arg = args[0]
				}
				t.Logf("raising value error...%v", arg)
				return None, NewValueError(fmt.Errorf("wrong value"))
			}),
	}

	script := `
magic_index = 3
def long_running(i):
	try:
		if i == magic_index:
			raise_value_error(42)
	except ValueError as e:
		return long_running_builtin("the_argument", the_key="the_value")
	try:
		try:
			raise_value_error(i)
		except ValueError as e:
			raise_value_error(-i)
	except Exception as e:
		log_error(-i, str(e))

def looptry():
	x = []
	for i in range(0, 10):
		try:
			x.append(1)
			if i == 5:
				break
		except:
			continue
	if len(x) != 6:
		raise_value_error()

	my_set = {x for x in range(0,3)}
	if type(my_set) != "set":
		raise_value_error(type(my_set))
	if len(my_set) != 3:
		raise_value_error(len(my_set))

looptry()

a = 1
b = 2
c = 3
sum_abc = a + b + c

l = [a, b, c]
d = {"abc": "abc"}

responses = [long_running(i) for i in range(0, 10)]
response = responses[magic_index]

struct_abc = struct_val.a + struct_val.b + struct_val.c

`
	thread := &Thread{Load: load}
	skylarktest.SetReporter(thread, t)

	result, err := ExecFile(thread, filename, script, predeclared)
	switch err := err.(type) {
	case *EvalError:
		t.Fatal(err.Backtrace())
	case nil:
		// success
		response := result["response"]
		if response != nil {
			t.Fatalf("Expected values defined after suspension to not be defined in result, found %v", response)
		}
	default:
		t.Error(err)
		return
	}

	suspended := thread.SuspendedFrame()
	if suspended == nil || suspended.Callable() != predeclared["long_running_builtin"] {
		t.Fatalf("Expected long_running_builtin() in top frame of suspended thread, found %s", suspended.Callable().Name())
	}

	snapshot, err := EncodeState(thread)
	if err != nil {
		t.Fatal(err)
	}
	compressedSize := len(snapshot)
	t.Logf("Encoded/compressed snapshot size: %dB", len(snapshot))

	snapshot, err = NewEncoder().DisableCompression().EncodeState(thread)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Encoded/uncompressed snapshot size: %dB", len(snapshot))
	t.Logf("Compression ratio: %.3f", float64(compressedSize)/float64(len(snapshot)))

	thread, err = DecodeState(snapshot, predeclared)
	if err != nil {
		t.Fatal(err)
		return
	}

	top := thread.TopFrame()
	if top.Callable() != predeclared["long_running_builtin"] {
		t.Fatalf("Expected long_running_builtin() in top frame of decoded state, found %v", top.Callable().Name())
	}
	if len(top.Args()) != 1 || top.Args()[0] != String("the_argument") {
		t.Fatalf("Expected arguments to be preserved after suspension/resumption, found args=%v", top.Args())
	}
	if len(top.Kwargs()) != 1 || top.Kwargs()[0][0] != String("the_key") || top.Kwargs()[0][1] != String("the_value") {
		t.Fatalf("Expected keyword arguments to be preserved after suspension/resumption, found kwargs=%v", top.Kwargs())
	}

	if thread.Caller().Callable().Name() != "long_running" {
		t.Fatalf("Expected long_running() in caller frame of decoded state, found %v", thread.Caller().Callable().Name())
	}

	response := String("abc123")

	result, err = Resume(thread, response)
	if err != nil {
		t.Fatalf("Error after resuming suspended thread: %v", err)
	}

	if result["response"] == nil || result["response"].(String) != response {
		t.Fatalf("Expected injected return value to be returned from suspending function after resuming, response=%v, responses=%#+v", result["response"], result["responses"])
	}
	sum, ok := result["sum_abc"].(Int)
	if i, ok2 := sum.Int64(); !ok || !ok2 || i != 6 {
		t.Fatal("Expected previously assigned global variables to be preserved after suspension/resumption")
	}
	if struct_abc, ok := result["struct_abc"].(String); !ok || struct_abc != String("abc") {
		t.Fatalf("Expected struct value to be preserved after suspension/resumption, struct_abc=%v", result["struct_abc"])
	}

	// Test resuming directly without serialization/deserialization:

	thread = &Thread{Load: load}
	skylarktest.SetReporter(thread, t)
	result, err = ExecFile(thread, filename, script, predeclared)
	if err != nil {
		t.Fatal(err)
	}
	result, err = Resume(thread, String("abc123"))
	if err != nil {
		t.Fatalf("Error after resuming suspended thread: %v", err)
	}
	if result["response"] == nil || result["response"].(String) != String("abc123") {
		t.Fatalf("Expected injected return value to be returned from suspending function after resuming")
	}
}
