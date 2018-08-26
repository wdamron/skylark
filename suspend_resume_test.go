// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark_test

import (
	"testing"

	"github.com/google/skylark"
	"github.com/google/skylark/skylarkstruct"
	"github.com/google/skylark/skylarktest"
)

func TestSuspendResume(t *testing.T) {
	filename := "suspend.sky"

	predeclared := skylark.StringDict{
		"struct_val": skylarkstruct.FromStringDict(skylarkstruct.Default, skylark.StringDict{
			"a": skylark.String("a"),
			"b": skylark.String("b"),
			"c": skylark.String("c"),
		}),
		"long_running_builtin": skylark.NewBuiltin("long_running_builtin",
			func(thread *skylark.Thread, fn *skylark.Builtin, args skylark.Tuple, kwargs []skylark.Tuple) (skylark.Value, error) {
				thread.Suspendable()
				return skylark.None, nil
			}),
	}

	script := `
def long_running(i):
	if i == 3:
		return long_running_builtin()
	else:
		return i

a = 1
b = 2
c = 3
sum_abc = a + b + c

l = [a, b, c]
struct_abc = struct_val.a + struct_val.b + struct_val.c # predeclared
d = {"abc": struct_abc}

responses = [long_running(i) for i in range(0, 10)]
response = responses[3]
`
	thread := &skylark.Thread{Load: load}
	skylarktest.SetReporter(thread, t)

	result, err := skylark.ExecFile(thread, filename, script, predeclared)
	switch err := err.(type) {
	case *skylark.EvalError:
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

	snapshot, err := skylark.EncodeState(thread)
	if err != nil {
		t.Fatal(err)
	}
	compressedSize := len(snapshot)
	t.Logf("Encoded/compressed snapshot size: %dB", len(snapshot))

	snapshot, err = skylark.NewEncoder().DisableCompression().EncodeState(thread)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Encoded/uncompressed snapshot size: %dB", len(snapshot))
	t.Logf("Compression ratio: %.3f", float64(compressedSize)/float64(len(snapshot)))

	thread, err = skylark.DecodeState(snapshot, predeclared)
	if err != nil {
		t.Fatal(err)
		return
	}

	if thread.TopFrame().Callable() != predeclared["long_running_builtin"] {
		t.Fatalf("Expected long_running_builtin() in top frame of decoded state, found %v", thread.TopFrame().Callable().Name())
	}

	if thread.Caller().Callable().Name() != "long_running" {
		t.Fatalf("Expected long_running() in caller frame of decoded state, found %v", thread.Caller().Callable().Name())
	}

	thread.Resumable()
	predeclared = thread.ToplevelFn().Predeclared()
	sval, ok := predeclared["struct_val"].(*skylarkstruct.Struct)
	if sval == nil || !ok {
		t.Fatalf("Missing predeclared struct value in decoded state, found %#+v", predeclared["struct_val"])
	}
	sdict := skylark.StringDict{}
	sval.ToStringDict(sdict)
	if sdict["a"] != skylark.String("a") || sdict["a"] != skylark.String("a") || sdict["a"] != skylark.String("a") {
		t.Fatalf("Missing entries in decoded struct value, found a=%v b=%v c=%v", sdict["a"], sdict["b"], sdict["c"])
	}

	response := skylark.String("abc123")

	result, err = skylark.Resume(thread, response)
	if err != nil {
		t.Fatalf("Error after resuming suspended thread: %v", err)
	}

	if result["response"] == nil || string(result["response"].(skylark.String)) != string(response) {
		t.Fatalf("Expected injected return value to be returned from suspending function after resuming, response=%v, responses=%#+v", result["response"], result["responses"])
	}
	sum, ok := result["sum_abc"].(skylark.Int)
	if i, ok2 := sum.Int64(); !ok || !ok2 || i != 6 {
		t.Fatal("Expected previously assigned global variables to be preserved after suspension/resumption")
	}
	struct_abc, _ := result["struct_abc"].(skylark.String)
	if struct_abc != skylark.String("abc") {
		t.Fatal("Expected previously assigned global variables to be preserved after suspension/resumption")
	}

	// Test resuming directly without serialization/deserialization:

	thread = &skylark.Thread{Load: load}
	skylarktest.SetReporter(thread, t)
	result, err = skylark.ExecFile(thread, filename, script, predeclared)
	if err != nil {
		t.Fatal(err)
	}
	result, err = skylark.Resume(thread, skylark.String("abc123"))
	if err != nil {
		t.Fatalf("Error after resuming suspended thread: %v", err)
	}
	if result["response"] == nil || result["response"].(skylark.String) != skylark.String("abc123") {
		t.Fatalf("Expected injected return value to be returned from suspending function after resuming")
	}
}
