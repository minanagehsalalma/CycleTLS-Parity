package cycletls

import (
	"bytes"
)

// writeU16 writes a 16-bit unsigned integer in big-endian format.
func writeU16(b *bytes.Buffer, v int) {
	b.WriteByte(byte(v >> 8))
	b.WriteByte(byte(v))
}

// writeStringWithLen writes a string with a 2-byte big-endian length prefix.
func writeStringWithLen(b *bytes.Buffer, s string) {
	writeU16(b, len(s))
	b.WriteString(s)
}

// writeRequestAndMethod writes the request ID and method name using length-prefixed format.
func writeRequestAndMethod(b *bytes.Buffer, requestID, method string) {
	writeStringWithLen(b, requestID)
	writeStringWithLen(b, method)
}

// buildErrorFrame creates an error response packet with status code and message.
func buildErrorFrame(requestID string, statusCode int, message string) []byte {
	var b bytes.Buffer
	writeRequestAndMethod(&b, requestID, "error")
	writeU16(&b, statusCode)
	writeStringWithLen(&b, message)
	return b.Bytes()
}

// buildEndFrame generates a frame signaling request completion.
func buildEndFrame(requestID string) []byte {
	var b bytes.Buffer
	writeRequestAndMethod(&b, requestID, "end")
	return b.Bytes()
}

// writeU32 writes a 32-bit unsigned integer in big-endian format.
func writeU32(b *bytes.Buffer, v int) {
	b.WriteByte(byte(v >> 24))
	b.WriteByte(byte(v >> 16))
	b.WriteByte(byte(v >> 8))
	b.WriteByte(byte(v))
}

// buildDataFrame packages response body data with a 4-byte length header.
func buildDataFrame(requestID string, body []byte) []byte {
	var b bytes.Buffer
	writeRequestAndMethod(&b, requestID, "data")
	writeU32(&b, len(body))
	b.Write(body)
	return b.Bytes()
}

// buildResponseFrame constructs the main response packet containing status code, final URL, and HTTP headers.
func buildResponseFrame(requestID string, statusCode int, finalURL string, headers map[string][]string) []byte {
	var b bytes.Buffer
	writeRequestAndMethod(&b, requestID, "response")
	writeU16(&b, statusCode)
	writeStringWithLen(&b, finalURL)

	// headers
	writeU16(&b, len(headers))
	for name, values := range headers {
		writeStringWithLen(&b, name)
		writeU16(&b, len(values))
		for _, v := range values {
			writeStringWithLen(&b, v)
		}
	}

	return b.Bytes()
}
