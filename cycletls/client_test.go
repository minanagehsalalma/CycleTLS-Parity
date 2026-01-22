//go:build !integration

package cycletls

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestGenerateClientKey_ReturnsHexString verifies that generateClientKey returns a valid hex string
func TestGenerateClientKey_ReturnsHexString(t *testing.T) {
	browser := Browser{
		JA3:       "771,52244-52243-52245,0-23-35-13,23-24,0",
		UserAgent: "Mozilla/5.0 Test",
	}

	key := generateClientKey(browser, 30, false, "")

	// Verify it's a valid hex string
	_, err := hex.DecodeString(key)
	if err != nil {
		t.Errorf("generateClientKey() returned invalid hex string: %s, error: %v", key, err)
	}
}

// TestGenerateClientKey_Deterministic verifies that same options produce same key
func TestGenerateClientKey_Deterministic(t *testing.T) {
	browser := Browser{
		JA3:                "771,52244-52243-52245,0-23-35-13,23-24,0",
		JA4r:               "t13d1516h2_8daaf6152771_02713d6af862",
		HTTP2Fingerprint:   "1:65536,2:0,3:1000,4:6291456,6:262144",
		QUICFingerprint:    "quic_fingerprint_test",
		UserAgent:          "Mozilla/5.0 Test Agent",
		ServerName:         "example.com",
		InsecureSkipVerify: true,
		ForceHTTP1:         false,
		ForceHTTP3:         true,
		Cookies: []Cookie{
			{Name: "session", Value: "abc123"},
			{Name: "token", Value: "xyz789"},
		},
	}

	key1 := generateClientKey(browser, 30, true, "http://proxy.example.com:8080")
	key2 := generateClientKey(browser, 30, true, "http://proxy.example.com:8080")
	key3 := generateClientKey(browser, 30, true, "http://proxy.example.com:8080")

	if key1 != key2 {
		t.Errorf("generateClientKey() not deterministic: key1=%s, key2=%s", key1, key2)
	}
	if key2 != key3 {
		t.Errorf("generateClientKey() not deterministic: key2=%s, key3=%s", key2, key3)
	}
}

// TestGenerateClientKey_DifferentOptionsDifferentKeys verifies that different options produce different keys
func TestGenerateClientKey_DifferentOptionsDifferentKeys(t *testing.T) {
	baseBrowser := Browser{
		JA3:       "771,52244-52243-52245,0-23-35-13,23-24,0",
		UserAgent: "Mozilla/5.0 Test",
	}
	baseTimeout := 30
	baseDisableRedirect := false
	baseProxyURL := ""

	baseKey := generateClientKey(baseBrowser, baseTimeout, baseDisableRedirect, baseProxyURL)

	diffJA3Browser := baseBrowser
	diffJA3Browser.JA3 = "771,49196-49195-49188,0-23-35-13,23-24,0"
	keyDiffJA3 := generateClientKey(diffJA3Browser, baseTimeout, baseDisableRedirect, baseProxyURL)
	if baseKey == keyDiffJA3 {
		t.Error("Different JA3 should produce different key")
	}

	keyDiffRedirect := generateClientKey(baseBrowser, baseTimeout, true, baseProxyURL)
	if baseKey == keyDiffRedirect {
		t.Error("Different disableRedirect should produce different key")
	}

	keyDiffProxy := generateClientKey(baseBrowser, baseTimeout, baseDisableRedirect, "http://proxy.example.com:8080")
	if baseKey == keyDiffProxy {
		t.Error("Different proxy should produce different key")
	}
}

// TestGenerateClientKey_KeyFormatValid verifies key format is valid lowercase hex
func TestGenerateClientKey_KeyFormatValid(t *testing.T) {
	browser := Browser{
		JA3:       "test",
		UserAgent: "test",
	}

	key := generateClientKey(browser, 30, false, "")

	if key != strings.ToLower(key) {
		t.Errorf("generateClientKey() should return lowercase hex, got: %s", key)
	}

	validHex := "0123456789abcdef"
	for _, c := range key {
		if !strings.ContainsRune(validHex, c) {
			t.Errorf("generateClientKey() contains invalid hex character: %c", c)
		}
	}
}

// TestGenerateClientKey_KeyLengthConsistent verifies key length is consistent (FNV-1a 64-bit = 16 hex chars)
func TestGenerateClientKey_KeyLengthConsistent(t *testing.T) {
	testCases := []struct {
		name    string
		browser Browser
		timeout int
		proxy   string
	}{
		{
			name:    "empty browser",
			browser: Browser{},
			timeout: 0,
			proxy:   "",
		},
		{
			name: "minimal browser",
			browser: Browser{
				JA3: "test",
			},
			timeout: 30,
			proxy:   "",
		},
		{
			name: "full browser config",
			browser: Browser{
				JA3:                "771,52244-52243-52245,0-23-35-13,23-24,0",
				JA4r:               "t13d1516h2_8daaf6152771_02713d6af862",
				HTTP2Fingerprint:   "1:65536,2:0,3:1000,4:6291456,6:262144",
				QUICFingerprint:    "quic_fingerprint_test",
				UserAgent:          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
				ServerName:         "example.com",
				InsecureSkipVerify: true,
				ForceHTTP1:         true,
				ForceHTTP3:         true,
				Cookies: []Cookie{
					{Name: "session", Value: "abc123"},
					{Name: "token", Value: "xyz789"},
				},
			},
			timeout: 120,
			proxy:   "http://user:pass@proxy.example.com:8080",
		},
	}

	expectedLength := 16 // FNV-1a 64-bit = 8 bytes = 16 hex chars

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := generateClientKey(tc.browser, tc.timeout, false, tc.proxy)
			if len(key) != expectedLength {
				t.Errorf("generateClientKey() key length = %d, expected %d", len(key), expectedLength)
			}
		})
	}
}

// TestGenerateClientKey_EmptyBrowser verifies handling of empty Browser struct
func TestGenerateClientKey_EmptyBrowser(t *testing.T) {
	browser := Browser{}
	key := generateClientKey(browser, 0, false, "")

	if key == "" {
		t.Error("generateClientKey() should not return empty string for empty browser")
	}

	_, err := hex.DecodeString(key)
	if err != nil {
		t.Errorf("generateClientKey() with empty browser returned invalid hex: %s", key)
	}

	if len(key) != 16 {
		t.Errorf("generateClientKey() with empty browser returned wrong length: %d", len(key))
	}
}

// TestGenerateClientKey_ConcurrentAccess verifies thread safety
func TestGenerateClientKey_ConcurrentAccess(t *testing.T) {
	browser := Browser{
		JA3:       "test_ja3",
		UserAgent: "test_ua",
	}

	expectedKey := generateClientKey(browser, 30, false, "")

	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			key := generateClientKey(browser, 30, false, "")
			if key != expectedKey {
				t.Errorf("Concurrent access produced different key: got %s, expected %s", key, expectedKey)
			}
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}
}
