package protocol

import (
	"bytes"
	"math"
	"testing"
)

// Issue #10: Integer overflow in writeUint16/writeUint32
// The int parameters can overflow uint16/uint32 max with no bounds checking.

func TestWriteUint16_ValidValues(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		expected []byte
	}{
		{"zero", 0, []byte{0x00, 0x00}},
		{"one", 1, []byte{0x00, 0x01}},
		{"max uint16", 65535, []byte{0xFF, 0xFF}},
		{"mid value", 256, []byte{0x01, 0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			err := safeWriteUint16(&b, tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(b.Bytes(), tt.expected) {
				t.Fatalf("got %v, want %v", b.Bytes(), tt.expected)
			}
		})
	}
}

func TestWriteUint16_Overflow(t *testing.T) {
	tests := []struct {
		name  string
		value int
	}{
		{"exceeds uint16 max", 65536},
		{"large positive", math.MaxInt32},
		{"negative", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			err := safeWriteUint16(&b, tt.value)
			if err == nil {
				t.Fatal("expected error for overflow value, got nil")
			}
		})
	}
}

func TestWriteUint32_ValidValues(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		expected []byte
	}{
		{"zero", 0, []byte{0x00, 0x00, 0x00, 0x00}},
		{"one", 1, []byte{0x00, 0x00, 0x00, 0x01}},
		{"max uint32", math.MaxUint32, []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{"mid value", 65536, []byte{0x00, 0x01, 0x00, 0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			err := safeWriteUint32(&b, tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(b.Bytes(), tt.expected) {
				t.Fatalf("got %v, want %v", b.Bytes(), tt.expected)
			}
		})
	}
}

func TestWriteUint32_Overflow(t *testing.T) {
	tests := []struct {
		name  string
		value int
	}{
		{"exceeds uint32 max on 64-bit", math.MaxUint32 + 1},
		{"negative", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			err := safeWriteUint32(&b, tt.value)
			if err == nil {
				t.Fatal("expected error for overflow value, got nil")
			}
		})
	}
}

func TestEncodeError_StillWorks(t *testing.T) {
	// Ensure existing EncodeError still works correctly after changes
	data := EncodeError("req-1", 500, "internal error")
	if len(data) == 0 {
		t.Fatal("expected non-empty encoded error frame")
	}
}

func TestEncodeResponse_StillWorks(t *testing.T) {
	headers := map[string][]string{
		"Content-Type": {"application/json"},
	}
	data := EncodeResponse("req-1", 200, "https://example.com", headers)
	if len(data) == 0 {
		t.Fatal("expected non-empty encoded response frame")
	}
}

func TestEncodeData_StillWorks(t *testing.T) {
	data := EncodeData("req-1", []byte("hello world"))
	if len(data) == 0 {
		t.Fatal("expected non-empty encoded data frame")
	}
}

func TestEncodeEnd_StillWorks(t *testing.T) {
	data := EncodeEnd("req-1")
	if len(data) == 0 {
		t.Fatal("expected non-empty encoded end frame")
	}
}
