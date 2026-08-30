package utils

import (
	"errors"
	"testing"

	"mizar/internal/agent"
)

func TestNormalizeAbortReason_AbortReason(t *testing.T) {
	r := NormalizeAbortReason(AbortUserAbort)
	if r != AbortUserAbort {
		t.Fatalf("expected %q, got %q", AbortUserAbort, r)
	}
}

func TestNormalizeAbortReason_UnknownAbortReason(t *testing.T) {
	r := NormalizeAbortReason(AbortReason("nonexistent"))
	if r != AbortUnknown {
		t.Fatalf("expected %q, got %q", AbortUnknown, r)
	}
}

func TestNormalizeAbortReason_String(t *testing.T) {
	tests := []struct {
		input    string
		expected AbortReason
	}{
		{"user-abort", AbortUserAbort},
		{"timeout", AbortTimeout},
		{"max-steps", AbortMaxSteps},
		{"background", AbortBackground},
		{"parent-ended", AbortParentEnded},
		{"tool-error", AbortToolError},
		{"llm-error", AbortLLMError},
		{"unknown", AbortUnknown},
		{"some-random-string", AbortUnknown},
	}
	for _, tt := range tests {
		r := NormalizeAbortReason(tt.input)
		if r != tt.expected {
			t.Errorf("NormalizeAbortReason(%q) = %q, want %q", tt.input, r, tt.expected)
		}
	}
}

func TestNormalizeAbortReason_Alias(t *testing.T) {
	tests := []struct {
		input    string
		expected AbortReason
	}{
		{"user-cancel", AbortUserAbort},
		{"hard-max", AbortMaxSteps},
		{"tool-timeout", AbortTimeout},
		{"side-task-cancelled", AbortParentEnded},
	}
	for _, tt := range tests {
		r := NormalizeAbortReason(tt.input)
		if r != tt.expected {
			t.Errorf("NormalizeAbortReason(%q) = %q, want %q", tt.input, r, tt.expected)
		}
	}
}

func TestNormalizeAbortReason_Error(t *testing.T) {
	tests := []struct {
		err      error
		expected AbortReason
	}{
		{agent.ErrAborted, AbortUserAbort},
		{agent.ErrTimeout, AbortTimeout},
		{agent.ErrMaxSteps, AbortMaxSteps},
		{errors.New("something else"), AbortUnknown},
	}
	for _, tt := range tests {
		r := NormalizeAbortReason(tt.err)
		if r != tt.expected {
			t.Errorf("NormalizeAbortReason(%v) = %q, want %q", tt.err, r, tt.expected)
		}
	}
}

func TestNormalizeAbortReason_NilError(t *testing.T) {
	r := NormalizeAbortReason(error(nil))
	if r != AbortUnknown {
		t.Fatalf("expected %q for nil error, got %q", AbortUnknown, r)
	}
}

func TestNormalizeAbortReason_UnknownType(t *testing.T) {
	r := NormalizeAbortReason(42)
	if r != AbortUnknown {
		t.Fatalf("expected %q for int, got %q", AbortUnknown, r)
	}
}

func TestAbortReasonMessage_AllReasons(t *testing.T) {
	reasons := []AbortReason{
		AbortUserAbort, AbortTimeout, AbortMaxSteps,
		AbortBackground, AbortParentEnded, AbortToolError,
		AbortLLMError, AbortUnknown,
	}
	for _, r := range reasons {
		msg := AbortReasonMessage(r)
		if msg == "" {
			t.Errorf("AbortReasonMessage(%q) is empty", r)
		}
	}
}

func TestAbortReasonMessage_UnknownDefault(t *testing.T) {
	msg := AbortReasonMessage(AbortReason("nonexistent"))
	if msg == "" {
		t.Fatal("expected non-empty message for unknown reason")
	}
}

func TestIsExpectedAbort(t *testing.T) {
	expected := []AbortReason{
		AbortUserAbort, AbortTimeout, AbortMaxSteps,
		AbortBackground, AbortParentEnded,
	}
	for _, r := range expected {
		if !IsExpectedAbort(r) {
			t.Errorf("IsExpectedAbort(%q) = false, want true", r)
		}
	}

	unexpected := []AbortReason{
		AbortToolError, AbortLLMError, AbortUnknown,
	}
	for _, r := range unexpected {
		if IsExpectedAbort(r) {
			t.Errorf("IsExpectedAbort(%q) = true, want false", r)
		}
	}
}

func TestKnownAbortReasons_Completeness(t *testing.T) {
	allReasons := []AbortReason{
		AbortUserAbort, AbortTimeout, AbortMaxSteps,
		AbortBackground, AbortParentEnded, AbortToolError,
		AbortLLMError, AbortUnknown,
	}
	for _, r := range allReasons {
		if _, ok := KnownAbortReasons[r]; !ok {
			t.Errorf("KnownAbortReasons missing %q", r)
		}
	}
	if len(KnownAbortReasons) != len(allReasons) {
		t.Errorf("KnownAbortReasons has %d entries, expected %d", len(KnownAbortReasons), len(allReasons))
	}
}
