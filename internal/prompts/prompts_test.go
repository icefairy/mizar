package prompts

import (
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	src := "---\ndescription: Review PRs from URLs\nargument-hint: <PR-URL>\n---\nReview the PR at $1.\n"
	desc, hint := parseFrontmatter(src)
	if desc != "Review PRs from URLs" {
		t.Fatalf("desc = %q, want Review PRs from URLs", desc)
	}
	if hint != "<PR-URL>" {
		t.Fatalf("hint = %q, want <PR-URL>", hint)
	}
}

func TestStripFrontmatter(t *testing.T) {
	src := "---\ndescription: test\n---\nHello $1 world"
	body := stripFrontmatter(src)
	want := "Hello $1 world"
	if body != want {
		t.Fatalf("got %q, want %q", body, want)
	}
}

func TestExpandSimple(t *testing.T) {
	r := NewRegistry()
	r.tpls["hello"] = &Template{
		Name: "hello", Content: "Hello, $1!",
	}
	out, err := r.Expand("hello", "World")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Hello, World!" {
		t.Fatalf("got %q, want Hello, World!", out)
	}
}

func TestExpandDefault(t *testing.T) {
	r := NewRegistry()
	r.tpls["sum"] = &Template{
		Name: "sum", Content: "Summarize in ${1:-5} points.",
	}
	out, err := r.Expand("sum", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Summarize in 5 points." {
		t.Fatalf("got %q, want Summarize in 5 points.", out)
	}
}

func TestExpandAt(t *testing.T) {
	r := NewRegistry()
	r.tpls["multi"] = &Template{
		Name: "multi", Content: "Args: $@ and $1 first.",
	}
	out, err := r.Expand("multi", "a b c")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Args: a b c and a first." {
		t.Fatalf("got %q, want Args: a b c and a first.", out)
	}
}

func TestExpandAtSlice(t *testing.T) {
	r := NewRegistry()
	r.tpls["slice"] = &Template{
		Name: "slice", Content: "${@:2:2}",
	}
	out, err := r.Expand("slice", "a b c d")
	if err != nil {
		t.Fatal(err)
	}
	if out != "b c" {
		t.Fatalf("got %q, want b c", out)
	}
}

func TestSplitArgs(t *testing.T) {
	parts := splitArgs(`hello "world foo" bar`)
	if len(parts) != 3 || parts[0] != "hello" || parts[1] != "world foo" || parts[2] != "bar" {
		t.Fatalf("got %v", parts)
	}
}
