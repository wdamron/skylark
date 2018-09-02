// Copyright 2018 West Damron. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sk8s_test

import (
	"testing"

	"github.com/google/skylark"
	"github.com/google/skylark/sk8s"
	"github.com/google/skylark/sk8s/kinds"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test(t *testing.T) {
	for k, _ := range sk8s.Library() {
		t.Log(k)
	}
	t.Log("Pod Fields:")
	for _, name := range kinds.Pod_attrs {
		t.Logf("%s: t=%v spec=%#+v\n", name, kinds.Pod_fields[name].FieldType.Name(), kinds.Pod_fields[name])
	}
	t.Log("Pod Inline Fields:")
	for _, name := range kinds.Pod_attrs {
		spec := kinds.Pod_fields[name]
		if !spec.Inline {
			continue
		}
		spec = kinds.Pod_inline[name]
		t.Logf("%s: t=%v spec=%#+v\n", name, kinds.Pod_inline[name].FieldType.Name(), kinds.Pod_inline[name])
	}

	v1pod := &v1.Pod{TypeMeta: metav1.TypeMeta{Kind: "Pod"}}

	for _, inner := range []interface{}{v1pod, *v1pod} {
		conv, converr := sk8s.ToSky(inner)
		if converr != nil {
			t.Fatal(converr)
		}
		pod := conv.(kinds.Pod)
		kind, err := pod.Attr("kind")
		if err != nil {
			t.Fatal(err)
		}
		if kind.(skylark.String) != skylark.String("Pod") {
			t.Fatalf("Expected kind = Pod\n")
		}

		t.Logf("Pod: %s\n", pod.String())
	}
}
