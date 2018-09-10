// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sk8s_test

import (
	"reflect"
	"testing"

	"github.com/google/skylark"
	. "github.com/google/skylark/sk8s"
	"github.com/google/skylark/skylarktest"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/diff"
)

func TestBasicInlineAttr(t *testing.T) {

	v1pod := &v1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod"},
	}

	for _, inner := range []interface{}{v1pod, *v1pod} {
		conv, err := ToSky(inner)
		if err != nil {
			t.Fatal(err)
		}
		pod := conv.(Kind)
		kind, err := pod.Attr("kind")
		if err != nil {
			t.Fatal(err)
		}
		if kind.(skylark.String) != "Pod" {
			t.Fatalf("Expected kind = Pod\n")
		}
		meta, err := pod.Attr("metadata")
		if err != nil {
			t.Fatal(err)
		}
		name, err := meta.(Kind).Attr("name")
		if err != nil {
			t.Fatal(err)
		}
		if name.(skylark.String) != "my-pod" {
			t.Fatalf("Expected name = my-pod\n")
		}
	}
}

func TestResourceSliceAttr(t *testing.T) {
	v1caps := &v1.Capabilities{
		Add: []v1.Capability{"my-capability"},
	}

	conv, err := ToSky(v1caps)
	if err != nil {
		t.Fatal(err)
	}

	caps := conv.(Kind)
	add, err := caps.Attr("add")
	if err != nil {
		t.Fatal(err)
	}

	list := add.(*skylark.List)
	if list.Len() == 0 {
		t.Fatalf("Expected a non-empty list of capabilities")
	}
	if list.Index(0).(skylark.String) != "my-capability" {
		t.Fatalf("Expected a list containing my-capability, found %#+v", list.Index(0))
	}
}

func TestBasicInlineSetAttr(t *testing.T) {
	v1pod := &v1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod"},
	}

	v, err := ToSky(v1pod)
	if err != nil {
		t.Fatal(err)
	}

	pod := v.(Kind)

	if err = pod.SetField("kind", skylark.String("NotPod")); err != nil {
		t.Fatal(err)
	}

	kind, err := pod.Attr("kind")
	if err != nil {
		t.Fatal(err)
	}
	if kind.(skylark.String) != "NotPod" {
		t.Fatalf("Expected kind = NotPod, found %v", kind)
	}
	if v1pod.TypeMeta.Kind != "NotPod" {
		t.Fatalf("Expected Kind = NotPod, found %s", v1pod.TypeMeta.Kind)
	}

	meta, err := pod.Attr("metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err = meta.(Kind).SetField("name", skylark.String("not-my-pod")); err != nil {
		t.Fatal(err)
	}
	name, err := meta.(Kind).Attr("name")
	if err != nil {
		t.Fatal(err)
	}
	if name.(skylark.String) != "not-my-pod" {
		t.Fatalf("Expected name = not-my-pod, found %v", name)
	}
	if v1pod.ObjectMeta.Name != "not-my-pod" {
		t.Fatalf("Expected Name = not-my-pod, found %s", v1pod.ObjectMeta.Name)
	}
}

func TestResourceSliceSetAttr(t *testing.T) {
	v1caps := &v1.Capabilities{}

	conv, err := ToSky(v1caps)
	if err != nil {
		t.Fatal(err)
	}

	caps := conv.(Kind)
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
	if list.Index(0).(skylark.String) != "my-capability" {
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
	predeclared := skylark.StringDict{}
	for name, builtin := range Library {
		predeclared[name] = builtin
	}

	script := `
caps = Capabilities({"add": ["my-capability"]})
caps.add += ["my-other-capability"]

meta = ObjectMeta(name="my-pod")

pod1 = Pod(kind="Pod", metadata=meta)

pod2 = Pod({"kind": "Pod", "metadata": meta})

pod3 = Pod({"kind": "Pod"}, metadata=meta)

pod4 = Pod(pod3)

pod5 = Pod(Pod(kind="Pod"), metadata=meta)

pod6 = Pod()
pod6.kind = "Pod"
pod6.metadata.name = meta.name

pod7 = Pod(None, kind="Pod", metadata=meta)

pod8 = Pod({}, kind="Pod", metadata=meta)

pod9 = Pod({}, kind="Pod", metadata={"name": meta.name})

pod10 = Pod({"kind": "Pod", "metadata": {"name": meta.name}})
`

	thread := &skylark.Thread{}
	skylarktest.SetReporter(thread, t)

	globals, err := skylark.ExecFile(thread, filename, script, predeclared)
	switch err := err.(type) {
	case *skylark.EvalError:
		t.Fatal(err.Backtrace())
	case nil:
		// success
	default:
		t.Error(err)
		return
	}

	caps := globals["caps"].(Kind)
	if caps.Type() != "Capabilities" {
		t.Errorf("expected type(caps) = Capabilities, found %s", caps.Type())
	}
	add, err := caps.Attr("add")
	if err != nil {
		t.Fatal(err)
	}
	if add.(*skylark.List).Index(0).(skylark.String) != "my-capability" {
		t.Errorf("expected caps.add[0] = my-capability, found add = %s", add.String())
	}
	if add.(*skylark.List).Index(1).(skylark.String) != "my-other-capability" {
		t.Errorf("expected caps.add[0] = my-other-capability, found add = %s", add.String())
	}

	for _, global := range []string{"pod1", "pod2", "pod3", "pod4", "pod5", "pod6", "pod7", "pod8", "pod9", "pod10"} {
		pod := globals[global].(Kind)

		kind, err := pod.Attr("kind")
		if err != nil {
			t.Fatal(err)
		}
		if kind.(skylark.String) != "Pod" {
			t.Fatalf("Expected kind = Pod\n")
		}
		meta, err := pod.Attr("metadata")
		if err != nil {
			t.Fatal(err)
		}
		name, err := meta.(Kind).Attr("name")
		if err != nil {
			t.Fatal(err)
		}
		if name.(skylark.String) != "my-pod" {
			t.Fatalf("Expected name = my-pod\n")
		}
	}

}

func TestNestedConstruction(t *testing.T) {
	filename := "sk8s_test.sky"
	predeclared := skylark.StringDict{}
	for name, builtin := range Library {
		predeclared[name] = builtin
	}

	script := `
deployment = Deployment({
	"metadata": {
		"name": "kafka-trigger-controller",
		"labels": {
			"kubeless": "kafka-trigger-controller"
		}
	},
	"spec": {
		"replicas": 3,
		"selector": {
		    "matchLabels": {
		    	"kubeless": "kafka-trigger-controller"
		    }
		}
	}})

pod = Pod({
	"metadata": {
		"name": "my-pod"
	},
	"spec": {
		"containers": [{"name": "my-container", "image": "my-image:latest", "imagePullPolicy": "Always"}]
	}})

pod.spec.containers[0].imagePullPolicy = "IfNotPresent"
`

	thread := &skylark.Thread{}
	skylarktest.SetReporter(thread, t)

	globals, err := skylark.ExecFile(thread, filename, script, predeclared)
	switch err := err.(type) {
	case *skylark.EvalError:
		t.Fatal(err.Backtrace())
	case nil:
		// success
	default:
		t.Error(err)
		return
	}

	depl := globals["deployment"].(Kind).Underlying().(*appsv1.Deployment)
	replicas := int32(3)
	expectedDepl := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kafka-trigger-controller",
			Labels: map[string]string{"kubeless": "kafka-trigger-controller"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubeless": "kafka-trigger-controller"},
			},
		},
	}

	if !reflect.DeepEqual(depl, expectedDepl) {
		t.Fatalf("Unexpected difference(s) in Deployment: %s", diff.ObjectReflectDiff(expectedDepl, depl))
	}

	pod := globals["pod"].(Kind).Underlying().(*v1.Pod)
	expectedPod := &v1.Pod{
		TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-pod",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				v1.Container{
					Name:            "my-container",
					Image:           "my-image:latest",
					ImagePullPolicy: v1.PullIfNotPresent,
				},
			},
		},
	}

	if !reflect.DeepEqual(expectedPod, pod) {
		t.Fatalf("Unexpected difference(s) in Pod: %s", diff.ObjectReflectDiff(expectedPod, pod))
	}

}
