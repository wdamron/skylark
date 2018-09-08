// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sk8s_test

import (
	"testing"

	"github.com/google/skylark"
	"github.com/google/skylark/sk8s"
	"github.com/google/skylark/sk8s/kinds"
	"github.com/google/skylark/skylarktest"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBasicInlineAttr(t *testing.T) {
	if len(kinds.Pod_attrs) == 0 {
		t.Fatalf("Expected Pod_attrs to contain attr names")
	}
	if len(kinds.Pod_fields) == 0 {
		t.Fatalf("Expected Pod_fields to contain field specs")
	}
	if len(kinds.Pod_inline) == 0 {
		t.Fatalf("Expected Pod_inline to contain inline-field specs")
	}

	v1pod := &v1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod"},
	}

	for _, inner := range []interface{}{v1pod, *v1pod} {
		conv, err := sk8s.ToSky(inner)
		if err != nil {
			t.Fatal(err)
		}
		pod := conv.(kinds.Pod)
		kind, err := pod.Attr("kind")
		if err != nil {
			t.Fatal(err)
		}
		if kind.(skylark.String) != skylark.String("Pod") {
			t.Fatalf("Expected kind = Pod\n")
		}
		meta, err := pod.Attr("metadata")
		if err != nil {
			t.Fatal(err)
		}
		name, err := meta.(kinds.ObjectMeta).Attr("name")
		if err != nil {
			t.Fatal(err)
		}
		if name.(skylark.String) != skylark.String("my-pod") {
			t.Fatalf("Expected name = my-pod\n")
		}

		t.Logf("Pod: %s\n", pod.String())
	}
}

func TestResourceSliceAttr(t *testing.T) {
	v1caps := &v1.Capabilities{
		Add: []v1.Capability{"my-capability"},
	}

	conv, err := sk8s.ToSky(v1caps)
	if err != nil {
		t.Fatal(err)
	}

	caps := conv.(kinds.Capabilities)
	add, err := caps.Attr("add")
	if err != nil {
		t.Fatal(err)
	}

	list := add.(*skylark.List)
	if list.Len() == 0 {
		t.Fatalf("Expected a non-empty list of capabilities")
	}
	if list.Index(0).(skylark.String) != skylark.String("my-capability") {
		t.Fatalf("Expected a list containing my-capability, found %#+v", list.Index(0))
	}
}

func TestBasicInlineSetAttr(t *testing.T) {
	v1pod := &v1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod"},
	}

	v, err := sk8s.ToSky(v1pod)
	if err != nil {
		t.Fatal(err)
	}

	pod := v.(kinds.Pod)

	if err = pod.SetField("kind", skylark.String("NotPod")); err != nil {
		t.Fatal(err)
	}

	kind, err := pod.Attr("kind")
	if err != nil {
		t.Fatal(err)
	}
	if kind.(skylark.String) != skylark.String("NotPod") {
		t.Fatalf("Expected kind = NotPod, found %v", kind)
	}
	if v1pod.TypeMeta.Kind != "NotPod" {
		t.Fatalf("Expected Kind = NotPod, found %s", v1pod.TypeMeta.Kind)
	}

	meta, err := pod.Attr("metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err = meta.(kinds.ObjectMeta).SetField("name", skylark.String("not-my-pod")); err != nil {
		t.Fatal(err)
	}
	name, err := meta.(kinds.ObjectMeta).Attr("name")
	if err != nil {
		t.Fatal(err)
	}
	if name.(skylark.String) != skylark.String("not-my-pod") {
		t.Fatalf("Expected name = not-my-pod, found %v", name)
	}
	if v1pod.ObjectMeta.Name != "not-my-pod" {
		t.Fatalf("Expected Name = not-my-pod, found %s", v1pod.ObjectMeta.Name)
	}
}

func TestResourceSliceSetAttr(t *testing.T) {
	v1caps := &v1.Capabilities{}

	conv, err := sk8s.ToSky(v1caps)
	if err != nil {
		t.Fatal(err)
	}

	caps := conv.(kinds.Capabilities)
	list := skylark.NewList([]skylark.Value{skylark.String("my-capability")})
	if err := caps.SetField("add", list); err != nil {
		t.Fatal(err)
	}

	add, err := caps.Attr("add")
	if err != nil {
		t.Fatal(err)
	}
	list = add.(*skylark.List)
	if list.Len() == 0 {
		t.Fatalf("Expected a non-empty list of capabilities")
	}
	if list.Index(0).(skylark.String) != skylark.String("my-capability") {
		t.Fatalf("Expected a list containing my-capability, found %#+v", list.Index(0))
	}
	if len(v1caps.Add) == 0 || string(v1caps.Add[0]) != "my-capability" {
		t.Fatalf("Expected my-capability to be added to Capabilities")
	}

	if err = caps.SetField("add", skylark.None); err != nil {
		t.Fatal(err)
	}
	if len(v1caps.Add) != 0 {
		t.Fatalf("Expected capabilities to me cleared when set to None")
	}
}

func TestConstructors(t *testing.T) {
	filename := "sk8s_test.sky"
	predeclared := skylark.StringDict{
		"Pod":        sk8s.Library["Pod"],
		"ObjectMeta": sk8s.Library["ObjectMeta"],
	}

	script := `
meta = ObjectMeta(name="my-pod")

pod1 = Pod(kind="Pod", metadata=meta)

pod2 = Pod({"kind": "Pod", "metadata": meta})

pod3 = Pod({"kind": "Pod"}, metadata=meta)

pod4 = Pod(pod3)

pod5 = Pod(Pod(kind="Pod"), metadata=meta)

pod6 = Pod()
pod6.kind = "Pod"
pod6.metadata.name = "my-pod"

pod7 = Pod(None, kind="Pod", metadata=meta)

pod8 = Pod({}, kind="Pod", metadata=meta)

pod9 = Pod({}, kind="Pod", metadata={"name": "my-pod"})
`

	thread := &skylark.Thread{}
	skylarktest.SetReporter(thread, t)

	result, err := skylark.ExecFile(thread, filename, script, predeclared)
	switch err := err.(type) {
	case *skylark.EvalError:
		t.Fatal(err.Backtrace())
	case nil:
		// success
	default:
		t.Error(err)
		return
	}

	for _, global := range []string{"pod1", "pod2", "pod3", "pod4", "pod5", "pod6", "pod7", "pod8", "pod9"} {
		pod := result[global].(kinds.Pod)

		kind, err := pod.Attr("kind")
		if err != nil {
			t.Fatal(err)
		}
		if kind.(skylark.String) != skylark.String("Pod") {
			t.Fatalf("Expected kind = Pod\n")
		}
		meta, err := pod.Attr("metadata")
		if err != nil {
			t.Fatal(err)
		}
		name, err := meta.(kinds.ObjectMeta).Attr("name")
		if err != nil {
			t.Fatal(err)
		}
		if name.(skylark.String) != skylark.String("my-pod") {
			t.Fatalf("Expected name = my-pod\n")
		}
	}

}
