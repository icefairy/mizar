package utils

import (
	"testing"
)

func TestStableJSON_MapKeyOrdering(t *testing.T) {
	// Go map iteration is random, but StableJSON should always produce sorted keys
	m := map[string]int{"z": 26, "a": 1, "m": 13}
	s1, err := StableJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	// 多次序列化结果一致
	for i := 0; i < 10; i++ {
		s2, err := StableJSON(m)
		if err != nil {
			t.Fatal(err)
		}
		if s1 != s2 {
			t.Fatalf("StableJSON not stable: %q vs %q", s1, s2)
		}
	}
	// 验证 key 已排序
	expected := `{"a":1,"m":13,"z":26}`
	if s1 != expected {
		t.Fatalf("expected %q, got %q", expected, s1)
	}
}

func TestStableJSON_NestedMap(t *testing.T) {
	m := map[string]interface{}{
		"b": map[string]int{"y": 2, "x": 1},
		"a": 42,
	}
	s, err := StableJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"a":42,"b":{"x":1,"y":2}}`
	if s != expected {
		t.Fatalf("expected %q, got %q", expected, s)
	}
}

func TestStableJSON_Slice(t *testing.T) {
	m := map[string]interface{}{
		"items": []int{3, 1, 2},
		"name":  "test",
	}
	s, err := StableJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"items":[3,1,2],"name":"test"}`
	if s != expected {
		t.Fatalf("expected %q, got %q", expected, s)
	}
}

func TestStableJSON_Primitive(t *testing.T) {
	s, err := StableJSON(42)
	if err != nil {
		t.Fatal(err)
	}
	if s != "42" {
		t.Fatalf("expected \"42\", got %q", s)
	}

	s, err = StableJSON("hello")
	if err != nil {
		t.Fatal(err)
	}
	if s != `"hello"` {
		t.Fatalf("expected '\"hello\"', got %q", s)
	}

	s, err = StableJSON(true)
	if err != nil {
		t.Fatal(err)
	}
	if s != "true" {
		t.Fatalf("expected \"true\", got %q", s)
	}
}

func TestStableJSON_EmptyMap(t *testing.T) {
	s, err := StableJSON(map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	if s != "{}" {
		t.Fatalf("expected \"{}\", got %q", s)
	}
}

func TestStableJSON_NilValue(t *testing.T) {
	s, err := StableJSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s != "null" {
		t.Fatalf("expected \"null\", got %q", s)
	}
}

func TestStableJSON_DeeplyNested(t *testing.T) {
	m := map[string]interface{}{
		"z_last": map[string]interface{}{
			"b": map[string]int{"b": 2, "a": 1},
			"a": "inner",
		},
		"a_first": 1,
	}
	s, err := StableJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"a_first":1,"z_last":{"a":"inner","b":{"a":1,"b":2}}}`
	if s != expected {
		t.Fatalf("expected %q, got %q", expected, s)
	}
}
