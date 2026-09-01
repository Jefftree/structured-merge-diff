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

package value

import (
	"reflect"
	"testing"
)

type identityHolder struct {
	SliceA []int             `json:"sliceA"`
	SliceB []int             `json:"sliceB"`
	MapA   map[string]string `json:"mapA"`
	MapB   map[string]string `json:"mapB"`
	AnyA   interface{}       `json:"anyA"`
	AnyB   interface{}       `json:"anyB"`
	I32    []int32           `json:"i32"`
	I64    []int64           `json:"i64"`
}

func TestSameUnderlying(t *testing.T) {
	arr := []int{1, 2, 3, 4, 5}
	sharedMap := map[string]string{"k": "v"}
	sharedAny := []interface{}{"x"}

	cases := []struct {
		name string
		set  func(h *identityHolder)
		a, b string
		want bool
	}{
		{"same slice header", func(h *identityHolder) { h.SliceA, h.SliceB = arr, arr }, "SliceA", "SliceB", true},
		{"different offsets", func(h *identityHolder) { h.SliceA, h.SliceB = arr[0:2], arr[1:3] }, "SliceA", "SliceB", false},
		{"different lengths", func(h *identityHolder) { h.SliceA, h.SliceB = arr[0:2], arr[0:3] }, "SliceA", "SliceB", false},
		{"different capacity", func(h *identityHolder) { h.SliceA, h.SliceB = arr[0:3], arr[0:3:4] }, "SliceA", "SliceB", true},
		{"prefix of a reusing append", func(h *identityHolder) {
			h.SliceA, h.SliceB = arr[0:3], append(arr[0:3], 99)[0:3]
		}, "SliceA", "SliceB", true},
		{"nil slices", func(h *identityHolder) {}, "SliceA", "SliceB", false},
		{"nil and empty slice", func(h *identityHolder) { h.SliceA = []int{} }, "SliceA", "SliceB", false},
		// Shares runtime.zerobase, so this is true. Sound: both are empty lists.
		{"two empty slices", func(h *identityHolder) {
			h.SliceA, h.SliceB = make([]int, 0), make([]int, 0)
		}, "SliceA", "SliceB", true},
		{"empty windows at different offsets", func(h *identityHolder) {
			h.SliceA, h.SliceB = arr[1:1], arr[2:2]
		}, "SliceA", "SliceB", false},
		// Also both at zerobase with length zero; only the type check rejects these.
		{"empty slices of different element types", func(h *identityHolder) {
			h.I32, h.I64 = make([]int32, 0), make([]int64, 0)
		}, "I32", "I64", false},
		{"same map header", func(h *identityHolder) { h.MapA, h.MapB = sharedMap, sharedMap }, "MapA", "MapB", true},
		{"equal but distinct maps", func(h *identityHolder) {
			h.MapA, h.MapB = map[string]string{"k": "v"}, map[string]string{"k": "v"}
		}, "MapA", "MapB", false},
		{"nil maps", func(h *identityHolder) {}, "MapA", "MapB", false},
		{"slice shared behind interfaces", func(h *identityHolder) { h.AnyA, h.AnyB = sharedAny, sharedAny }, "AnyA", "AnyB", true},
		{"distinct slices behind interfaces", func(h *identityHolder) {
			h.AnyA, h.AnyB = []interface{}{"x"}, []interface{}{"x"}
		}, "AnyA", "AnyB", false},
		{"nil interfaces", func(h *identityHolder) {}, "AnyA", "AnyB", false},
	}

	field := func(t *testing.T, h *identityHolder, name string) Value {
		t.Helper()
		v, err := wrapValueReflect(reflect.ValueOf(h).Elem().FieldByName(name), nil, nil)
		if err != nil {
			t.Fatalf("wrapValueReflect(%s): %v", name, err)
		}
		return v
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &identityHolder{}
			tc.set(h)
			a, b := field(t, h, tc.a), field(t, h, tc.b)
			if got := SameUnderlying(a, b); got != tc.want {
				t.Errorf("SameUnderlying() = %v, want %v", got, tc.want)
			}
			if SameUnderlying(a, b) != SameUnderlying(b, a) {
				t.Error("SameUnderlying() is not symmetric")
			}
			if SameUnderlying(a, b) && !Equals(a, b) {
				t.Errorf("SameUnderlying() reported true for unequal values: %v vs %v",
					a.Unstructured(), b.Unstructured())
			}
		})
	}

	u := NewValueInterface([]interface{}{int64(1)})
	if SameUnderlying(u, u) {
		t.Error("SameUnderlying() = true for unstructured values, want false")
	}
	seven, err := wrapValueReflect(reflect.ValueOf(7), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if SameUnderlying(seven, seven) {
		t.Error("SameUnderlying() = true for a scalar, want false")
	}
}

func TestSameUnderlyingStructCopy(t *testing.T) {
	type spec struct {
		List []int             `json:"list"`
		Map  map[string]string `json:"map"`
		Leaf int               `json:"leaf"`
	}
	type object struct {
		Spec spec `json:"spec"`
	}

	old := &object{Spec: spec{List: []int{1, 2, 3}, Map: map[string]string{"a": "b"}, Leaf: 5}}
	newer := &object{}
	newer.Spec = old.Spec
	old.Spec.List[1] = 99

	wrap := func(v reflect.Value) Value {
		w, err := wrapValueReflect(v, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	oldSpec := reflect.ValueOf(old).Elem().Field(0)
	newSpec := reflect.ValueOf(newer).Elem().Field(0)

	for name, want := range map[string]bool{"List": true, "Map": true, "Leaf": false} {
		a, b := wrap(oldSpec.FieldByName(name)), wrap(newSpec.FieldByName(name))
		if got := SameUnderlying(a, b); got != want {
			t.Errorf("SameUnderlying(spec.%s) = %v, want %v", name, got, want)
		} else if got && !Equals(a, b) {
			t.Errorf("SameUnderlying(spec.%s) reported true for unequal values", name)
		}
	}
	if SameUnderlying(wrap(oldSpec), wrap(newSpec)) {
		t.Error("SameUnderlying(spec) = true, want false")
	}
}

func TestSameUnderlyingNilValue(t *testing.T) {
	v, err := wrapValueReflect(reflect.ValueOf([]int{1}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ a, b Value }{{nil, nil}, {v, nil}, {nil, v}} {
		if SameUnderlying(tc.a, tc.b) {
			t.Errorf("SameUnderlying(%v, %v) = true, want false", tc.a, tc.b)
		}
	}
}
