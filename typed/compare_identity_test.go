/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package typed_test

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/randfill"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
)

var identitySchema = typed.YAMLObject(`types:
- name: object
  map:
    fields:
    - name: metadata
      type:
        namedType: metadata
    - name: spec
      type:
        namedType: spec
    - name: status
      type:
        namedType: status
- name: metadata
  map:
    fields:
    - name: name
      type:
        scalar: string
    - name: labels
      type:
        map:
          elementType:
            scalar: string
- name: spec
  map:
    fields:
    - name: replicas
      type:
        scalar: numeric
    - name: containers
      type:
        list:
          elementType:
            namedType: container
          elementRelationship: associative
          keys:
          - name
- name: container
  map:
    fields:
    - name: name
      type:
        scalar: string
    - name: image
      type:
        scalar: string
    - name: args
      type:
        list:
          elementType:
            scalar: string
          elementRelationship: atomic
    - name: env
      type:
        list:
          elementType:
            namedType: env
          elementRelationship: associative
          keys:
          - name
- name: env
  map:
    fields:
    - name: name
      type:
        scalar: string
    - name: value
      type:
        scalar: string
- name: status
  map:
    fields:
    - name: phase
      type:
        scalar: string
`)

type idObject struct {
	Metadata idMetadata `json:"metadata"`
	Spec     idSpec     `json:"spec"`
	Status   idStatus   `json:"status"`
}

type idMetadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type idSpec struct {
	Replicas   int64         `json:"replicas"`
	Containers []idContainer `json:"containers,omitempty"`
}

type idContainer struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Args  []string `json:"args,omitempty"`
	Env   []idEnv  `json:"env,omitempty"`
}

type idEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type idStatus struct {
	Phase string `json:"phase"`
}

func identityParser(t testing.TB) typed.ParseableType {
	t.Helper()
	p, err := typed.NewParser(identitySchema)
	if err != nil {
		t.Fatalf("failed to create parser: %v", err)
	}
	return p.Type("object")
}

// deepCopy shares nothing with o, so the identity shortcut cannot fire between them.
func deepCopy(o *idObject) *idObject {
	out := &idObject{}
	reflect.ValueOf(out).Elem().Set(deepCopyReflect(reflect.ValueOf(o).Elem()))
	return out
}

func deepCopyReflect(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopyReflect(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		// Empty slices must not land on the shared runtime.zerobase.
		out := reflect.MakeSlice(v.Type(), v.Len(), max(v.Len(), 1))
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopyReflect(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for iter := v.MapRange(); iter.Next(); {
			out.SetMapIndex(iter.Key(), deepCopyReflect(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			out.Field(i).Set(deepCopyReflect(v.Field(i)))
		}
		return out
	default:
		return v
	}
}

func compareStructured(pt typed.ParseableType, lhs, rhs *idObject) (*typed.Comparison, error) {
	ltv, err := pt.FromStructured(lhs, typed.AllowDuplicates)
	if err != nil {
		return nil, fmt.Errorf("lhs: %w", err)
	}
	rtv, err := pt.FromStructured(rhs, typed.AllowDuplicates)
	if err != nil {
		return nil, fmt.Errorf("rhs: %w", err)
	}
	return ltv.Compare(rtv)
}

// checkSharingInvariant asserts that Compare returns the same answer whether or
// not lhs and rhs share memory.
func checkSharingInvariant(t *testing.T, pt typed.ParseableType, lhs, rhs *idObject) *typed.Comparison {
	t.Helper()
	shared, sharedErr := compareStructured(pt, lhs, rhs)
	copied, copiedErr := compareStructured(pt, deepCopy(lhs), deepCopy(rhs))

	if (sharedErr == nil) != (copiedErr == nil) {
		t.Fatalf("Compare errored on one arm only:\n shared: %v\n copied: %v", sharedErr, copiedErr)
	}
	if sharedErr != nil {
		return nil
	}
	for _, f := range []struct {
		name           string
		shared, copied *fieldpath.Set
	}{
		{"Removed", shared.Removed, copied.Removed},
		{"Modified", shared.Modified, copied.Modified},
		{"Added", shared.Added, copied.Added},
	} {
		if !f.shared.Equals(f.copied) {
			t.Errorf("%s differs with and without sharing:\n shared:\n%v\n copied:\n%v",
				f.name, f.shared, f.copied)
		}
	}
	return shared
}

func TestCompareIdentity(t *testing.T) {
	pt := identityParser(t)
	base := func() *idObject {
		return &idObject{
			Metadata: idMetadata{Name: "obj", Labels: map[string]string{"a": "1"}},
			Spec: idSpec{
				Replicas: 3,
				Containers: []idContainer{
					{Name: "main", Image: "image:1", Args: []string{"--a"}, Env: []idEnv{{Name: "A", Value: "1"}}},
					{Name: "sidecar", Image: "image:2"},
				},
			},
			Status: idStatus{Phase: "Running"},
		}
	}

	cases := []struct {
		name  string
		build func() (lhs, rhs *idObject)
		want  string
	}{{
		name: "shared spec, changed status",
		build: func() (*idObject, *idObject) {
			old := base()
			newer := deepCopy(old)
			newer.Spec = old.Spec
			newer.Status.Phase = "Succeeded"
			return old, newer
		},
		want: "modified:.status.phase",
	}, {
		name: "shared containers, changed replicas",
		build: func() (*idObject, *idObject) {
			old := base()
			newer := deepCopy(old)
			newer.Spec.Containers = old.Spec.Containers
			newer.Spec.Replicas = 5
			return old, newer
		},
		want: "modified:.spec.replicas",
	}, {
		name: "shared labels map",
		build: func() (*idObject, *idObject) {
			old := base()
			newer := deepCopy(old)
			newer.Metadata.Labels = old.Metadata.Labels
			newer.Metadata.Name = "renamed"
			return old, newer
		},
		want: "modified:.metadata.name",
	}, {
		name: "containers sub-slice at an offset",
		build: func() (*idObject, *idObject) {
			old := base()
			newer := deepCopy(old)
			newer.Spec.Containers = old.Spec.Containers[1:]
			return old, newer
		},
		want: `removed:.spec.containers[name="main"]`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lhs, rhs := tc.build()
			got := checkSharingInvariant(t, pt, lhs, rhs)
			if tc.want == "" {
				if !got.IsSame() {
					t.Fatalf("expected no difference, got:\n%v", got)
				}
				return
			}
			kind, path, _ := strings.Cut(tc.want, ":")
			set := got.Modified
			if kind == "removed" {
				set = got.Removed
			}
			if !strings.Contains(set.String(), path) {
				t.Errorf("expected %s in %s, got:\n%v", path, kind, set)
			}
		})
	}
}

// The deduced schema runs alongside the real one because it makes every map and
// list atomic, sending the shortcut down the doLeaf path instead of the recursive one.
func TestCompareIdentityFuzz(t *testing.T) {
	parsers := map[string]typed.ParseableType{
		"schema":  identityParser(t),
		"deduced": typed.DeducedParseableType,
	}
	for seed := int64(0); seed < 2000; seed++ {
		lhs, rhs := fuzzObjectPair(seed)
		for name, pt := range parsers {
			t.Run(fmt.Sprintf("%s/seed=%d", name, seed), func(t *testing.T) {
				checkSharingInvariant(t, pt, lhs, rhs)
			})
		}
	}
}

func fuzzObjectPair(seed int64) (*idObject, *idObject) {
	rnd := rand.New(rand.NewSource(seed))
	f := randfill.New().RandSource(rand.NewSource(seed)).NilChance(0.2).NumElements(0, 4).MaxDepth(6)

	lhs := &idObject{}
	f.Fill(lhs)
	rhs := &idObject{}
	if rnd.Intn(2) == 0 {
		rhs = deepCopy(lhs)
	} else {
		f.Fill(rhs)
	}

	if rnd.Intn(2) == 0 {
		rhs.Spec = lhs.Spec
	}
	if rnd.Intn(3) == 0 {
		rhs.Spec.Containers = lhs.Spec.Containers
	}
	if n := len(lhs.Spec.Containers); n > 1 && rnd.Intn(3) == 0 {
		rhs.Spec.Containers = lhs.Spec.Containers[rnd.Intn(n):]
	}
	if rnd.Intn(3) == 0 {
		rhs.Metadata.Labels = lhs.Metadata.Labels
	}
	if len(rhs.Spec.Containers) > 0 && len(lhs.Spec.Containers) > 0 && rnd.Intn(3) == 0 {
		rhs.Spec.Containers = append([]idContainer(nil), rhs.Spec.Containers...)
		rhs.Spec.Containers[0].Env = lhs.Spec.Containers[0].Env
	}

	switch rnd.Intn(4) {
	case 0:
		rhs.Status.Phase = fmt.Sprintf("phase-%d", seed)
	case 1:
		rhs.Spec.Replicas = lhs.Spec.Replicas + 1
	case 2:
		rhs.Metadata.Name = fmt.Sprintf("name-%d", seed)
	}
	return lhs, rhs
}

// The shortcut changes the answer here, deliberately: value.Equals compares floats
// with ==, so without it a NaN leaf reports itself modified against an unchanged
// object. Do not "fix" either arm without deciding which behaviour is wanted.
func TestCompareIdentityNaN(t *testing.T) {
	type holder struct {
		Values []float64 `json:"values"`
	}
	pt := typed.DeducedParseableType
	parse := func(h *holder) *typed.TypedValue {
		t.Helper()
		tv, err := pt.FromStructured(h)
		if err != nil {
			t.Fatalf("FromStructured: %v", err)
		}
		return tv
	}

	lhs := &holder{Values: []float64{1, math.NaN()}}
	base := parse(lhs)

	if got, err := base.Compare(parse(&holder{Values: lhs.Values})); err != nil {
		t.Fatal(err)
	} else if !got.IsSame() {
		t.Errorf("with the values shared, expected no difference, got:\n%v", got)
	}
	if got, err := base.Compare(parse(&holder{Values: []float64{1, math.NaN()}})); err != nil {
		t.Fatal(err)
	} else if got.IsSame() {
		t.Error("without sharing, expected NaN to report itself as modified")
	}
}

// The shared arm assigns the spec across the way PrepareForUpdate does; the copied
// arm is the same content unshared, and measures the check when it never fires.
func BenchmarkCompareSharedSpec(b *testing.B) {
	pt := identityParser(b)
	for _, containers := range []int{1, 4, 7} {
		for _, shared := range []bool{true, false} {
			name := fmt.Sprintf("containers=%d/shared=%v", containers, shared)
			b.Run(name, func(b *testing.B) {
				live := benchmarkObject(containers)
				updated := deepCopy(live)
				if shared {
					updated.Spec = live.Spec
				}
				updated.Status.Phase = "Succeeded"

				liveTV, err := pt.FromStructured(live)
				if err != nil {
					b.Fatal(err)
				}
				updatedTV, err := pt.FromStructured(updated)
				if err != nil {
					b.Fatal(err)
				}

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					c, err := liveTV.Compare(updatedTV)
					if err != nil {
						b.Fatal(err)
					}
					if c.Modified.Size() != 1 {
						b.Fatalf("expected one modified field, got:\n%v", c.Modified)
					}
				}
			})
		}
	}
}

func benchmarkObject(containers int) *idObject {
	o := &idObject{
		Metadata: idMetadata{Name: "obj", Labels: map[string]string{"app": "bench"}},
		Spec:     idSpec{Replicas: 1},
		Status:   idStatus{Phase: "Running"},
	}
	for c := 0; c < containers; c++ {
		container := idContainer{
			Name:  fmt.Sprintf("container-%d", c),
			Image: fmt.Sprintf("registry.example.com/image-%d:v1.2.3", c),
			Args:  []string{"--v=2", "--logtostderr"},
		}
		for e := 0; e < 11; e++ {
			container.Env = append(container.Env, idEnv{
				Name:  fmt.Sprintf("ENV_VAR_%d", e),
				Value: fmt.Sprintf("value-of-env-var-%d", e),
			})
		}
		o.Spec.Containers = append(o.Spec.Containers, container)
	}
	return o
}
