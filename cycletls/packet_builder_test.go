package cycletls

import (
	"bytes"
	"testing"
)

// Issue #2: writeU16 integer overflow - values > 65535 should error
func TestWriteU16_Overflow(t *testing.T) {
	var b bytes.Buffer
	err := writeU16(&b, 70000) // exceeds uint16 max (65535)
	if err == nil {
		t.Fatal("writeU16 should return error for values > 65535")
	}
}

func TestWriteU16_MaxValid(t *testing.T) {
	var b bytes.Buffer
	err := writeU16(&b, 65535) // max uint16
	if err != nil {
		t.Fatalf("writeU16 should succeed for 65535, got %v", err)
	}
	if b.Len() != 2 {
		t.Fatalf("expected 2 bytes, got %d", b.Len())
	}
}

func TestWriteU16_Zero(t *testing.T) {
	var b bytes.Buffer
	err := writeU16(&b, 0)
	if err != nil {
		t.Fatalf("writeU16 should succeed for 0, got %v", err)
	}
	data := b.Bytes()
	if data[0] != 0 || data[1] != 0 {
		t.Fatalf("expected [0, 0], got %v", data)
	}
}

func TestWriteU16_Negative(t *testing.T) {
	var b bytes.Buffer
	err := writeU16(&b, -1) // negative values should error
	if err == nil {
		t.Fatal("writeU16 should return error for negative values")
	}
}

// Issue #2: writeU32 integer overflow
func TestWriteU32_Overflow(t *testing.T) {
	var b bytes.Buffer
	err := writeU32(&b, int(1<<32)) // exceeds uint32 max
	if err == nil {
		t.Fatal("writeU32 should return error for values > 4294967295")
	}
}

func TestWriteU32_MaxValid(t *testing.T) {
	var b bytes.Buffer
	err := writeU32(&b, int(1<<32-1)) // max uint32
	if err != nil {
		t.Fatalf("writeU32 should succeed for max uint32, got %v", err)
	}
	if b.Len() != 4 {
		t.Fatalf("expected 4 bytes, got %d", b.Len())
	}
}

func TestWriteU32_Negative(t *testing.T) {
	var b bytes.Buffer
	err := writeU32(&b, -1)
	if err == nil {
		t.Fatal("writeU32 should return error for negative values")
	}
}

// Issue #3: buildResponseFrame header map bounds check
func TestBuildResponseFrame_TooManyHeaders(t *testing.T) {
	headers := make(map[string][]string)
	// Create more than 65535 headers
	for i := 0; i < 65536; i++ {
		key := "Header-" + string(rune(i/256+'A')) + string(rune(i%256+'A'))
		headers[key] = []string{"value"}
	}
	_, err := buildResponseFrame("req-1", 200, "http://example.com", headers)
	if err == nil {
		t.Fatal("buildResponseFrame should return error when headers exceed 65535")
	}
}

func TestBuildResponseFrame_MaxHeaders(t *testing.T) {
	// A small map should work fine
	headers := map[string][]string{
		"Content-Type": {"text/html"},
	}
	_, err := buildResponseFrame("req-1", 200, "http://example.com", headers)
	if err != nil {
		t.Fatalf("buildResponseFrame should succeed with small headers, got %v", err)
	}
}

// Issue #5: buildWebSocketOpenFrame should handle json.Marshal error
func TestBuildWebSocketOpenFrame_Success(t *testing.T) {
	data, err := buildWebSocketOpenFrame("req-1", "proto", "ext")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty frame")
	}
}

// Issue #5: buildWebSocketCloseFrame should handle json.Marshal error
func TestBuildWebSocketCloseFrame_Success(t *testing.T) {
	data, err := buildWebSocketCloseFrame("req-1", 1000, "normal")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty frame")
	}
}
