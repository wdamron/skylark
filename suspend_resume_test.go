// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark_test

import (
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
		"Exception":  String("Exception"),
		"ValueError": String("ValueError"),
		"log_error": NewBuiltin("print",
			func(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
				if len(args) > 0 {
					i, _ := args[0].(Int)
					s, _ := args[1].(String)
					t.Logf("i=%s err: %s", i.String(), string(s))
				}
				return None, nil
			}),
	}

	script := `
magic_index = 3
def long_running(i):
	if i == magic_index:
		try:
			x = 1 / (i - magic_index)
			return i
		except:
			try:
				return long_running_builtin("the_argument", the_key="the_value")
			except:
				return i
	try:
		x = 1 / i
	except Exception as e:
		log_error(i, e)
		return i

def looptry():
	x = []
	for i in range(0, 10):
		try:
			x.append(1)
			if i == 5:
				break
		except:
			continue
	x[5] += 1
	try:
		i = 1 / (len(x) - 6)
	except Exception as e:
		log_error(len(x), e)

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
