package cycletls

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	nhttp "net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Danny-Dasilva/CycleTLS/cycletls/state"
	http "github.com/Danny-Dasilva/fhttp"
	"github.com/gorilla/websocket"
	utls "github.com/refraction-networking/utls"
)

// safeChannelWriter wraps a channel to provide thread-safe writes with closed state tracking
type safeChannelWriter struct {
	ch     chan []byte
	mu     sync.RWMutex
	closed bool
}

// newSafeChannelWriter creates a new safe channel writer
func newSafeChannelWriter(ch chan []byte) *safeChannelWriter {
	return &safeChannelWriter{
		ch:     ch,
		closed: false,
	}
}

// write safely writes data to the channel, returning false if channel is closed
func (scw *safeChannelWriter) write(data []byte) bool {
	scw.mu.RLock()
	defer scw.mu.RUnlock()

	if scw.closed {
		return false
	}

	// Use defer/recover to handle panics from closed channel
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic writing to channel: %v", r)
		}
	}()

	scw.ch <- data
	return true
}

// close marks the channel as closed (does not actually close it to avoid double-close panics)
func (scw *safeChannelWriter) setClosed() {
	scw.mu.Lock()
	defer scw.mu.Unlock()
	scw.closed = true
}

// Type definitions moved to types.go

// Backward-compatible aliases to state package
var debugLogger = state.DebugLogger

// getBuffer retrieves a buffer from the pool and resets it for reuse
func getBuffer() *bytes.Buffer {
	return state.GetBuffer()
}

// putBuffer returns a buffer to the pool for reuse
func putBuffer(buf *bytes.Buffer) {
	state.PutBuffer(buf)
}

// WebSocket connection management
type WebSocketConnection struct {
	Conn        *websocket.Conn
	RequestID   string
	URL         string
	ReadyState  int // 0=CONNECTING, 1=OPEN, 2=CLOSING, 3=CLOSED
	mu          sync.RWMutex
	writeMu     sync.Mutex // for thread-safe writes to WebSocket connection
	commandChan chan WebSocketCommand
	closeChan   chan struct{}
	done        chan struct{}  // signals all goroutines to exit
	wg          sync.WaitGroup // tracks goroutine completion for clean shutdown
	chanWrite   *safeChannelWriter
	protocol    string // Negotiated subprotocol
	extensions  string // Negotiated extensions
}

// safeWrite performs a thread-safe write to the WebSocket connection.
// Gorilla WebSocket is NOT thread-safe for concurrent writes, so all writes
// must be serialized through this method.
func (ws *WebSocketConnection) safeWrite(conn *websocket.Conn, messageType int, data []byte) error {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	return conn.WriteMessage(messageType, data)
}

type WebSocketCommand struct {
	Type        string // "send", "close", "ping", "pong"
	Data        []byte
	IsBinary    bool
	CloseCode   int
	CloseReason string
}

// activeWebSockets is now managed by the state package
// Use state.RegisterWebSocket, state.GetWebSocket, state.UnregisterWebSocket

// browserFromOptions creates a Browser configuration from request options
func browserFromOptions(opts Options) Browser {
	return Browser{
		JA3:                opts.Ja3,
		JA4r:               opts.Ja4r,
		HTTP2Fingerprint:   opts.HTTP2Fingerprint,
		QUICFingerprint:    opts.QUICFingerprint,
		DisableGrease:      opts.DisableGrease,
		UserAgent:          opts.UserAgent,
		ServerName:         opts.ServerName,
		Cookies:            opts.Cookies,
		InsecureSkipVerify: opts.InsecureSkipVerify,
		ForceHTTP1:         opts.ForceHTTP1,
		ForceHTTP3:         opts.ForceHTTP3,
		TLS13AutoRetry:     opts.TLS13AutoRetry,
		HeaderOrder:        opts.HeaderOrder,
	}
}

// buildHTTPRequest creates an HTTP request from the options with the given context.
// This is used by both v1 and v2 code paths.
func buildHTTPRequest(request cycleTLSRequest, ctx context.Context) (*http.Request, error) {
	// Handle both string body and byte body
	var bodyReader io.Reader
	if len(request.Options.BodyBytes) > 0 {
		bodyReader = bytes.NewReader(request.Options.BodyBytes)
	} else {
		bodyReader = strings.NewReader(request.Options.Body)
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(request.Options.Method), request.Options.URL, bodyReader)
	if err != nil {
		return nil, err
	}

	// Build header order
	headerorder := []string{}
	if len(request.Options.HeaderOrder) > 0 {
		for _, v := range request.Options.HeaderOrder {
			headerorder = append(headerorder, strings.ToLower(v))
		}
	} else {
		headerorder = []string{
			"host", "connection", "cache-control", "device-memory", "viewport-width",
			"rtt", "downlink", "ect", "sec-ch-ua", "sec-ch-ua-mobile",
			"sec-ch-ua-full-version", "sec-ch-ua-arch", "sec-ch-ua-platform",
			"sec-ch-ua-platform-version", "sec-ch-ua-model", "upgrade-insecure-requests",
			"user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
			"sec-fetch-dest", "referer", "accept-encoding", "accept-language", "cookie",
		}
	}

	// Build header order key
	headerorderkey := []string{}
	for _, key := range headerorder {
		for k := range request.Options.Headers {
			if key == strings.ToLower(k) {
				headerorderkey = append(headerorderkey, strings.ToLower(k))
			}
		}
	}

	headerOrder := parseUserAgent(request.Options.UserAgent).HeaderOrder

	// Set headers with ordering
	req.Header = http.Header{
		http.HeaderOrderKey: headerorderkey,
	}

	// Only set PHeaderOrderKey for HTTP/2, not HTTP/3
	if !request.Options.ForceHTTP3 && request.Options.Protocol != "http3" {
		req.Header[http.PHeaderOrderKey] = headerOrder
	}

	// Parse URL for Host header
	u, err := url.Parse(request.Options.URL)
	if err != nil {
		return nil, err
	}

	// Append headers
	for k, v := range request.Options.Headers {
		if k != "Content-Length" {
			req.Header.Set(k, v)
		}
	}

	// Set Host header (respect user-provided for domain fronting)
	if _, ok := request.Options.Headers["Host"]; !ok {
		if _, ok := request.Options.Headers["host"]; !ok {
			req.Header.Set("Host", u.Host)
		}
	}
	req.Header.Set("user-agent", request.Options.UserAgent)

	return req, nil
}

// ready Request
func processRequest(request cycleTLSRequest) (result fullRequest) {
	// Handle protocol-specific clients first (they build their own browser config)
	switch {
	case request.Options.Protocol == "websocket":
		return dispatchWebSocketRequest(request)
	case request.Options.Protocol == "sse":
		return dispatchSSERequest(request)
	case request.Options.Protocol == "http3" || request.Options.ForceHTTP3:
		return dispatchHTTP3Request(request)
	}

	ctx, cancel := context.WithCancel(context.Background())
	browser := browserFromOptions(request.Options)

	// Connection reuse is enabled by default
	client, err := newClientWithReuse(
		browser,
		request.Options.Timeout,
		request.Options.DisableRedirect,
		request.Options.UserAgent,
		request.Options.EnableConnectionReuse != false,
		request.Options.Proxy,
	)
	if err != nil {
		log.Fatal(err)
	}

	// Handle both string body and byte body
	var bodyReader io.Reader
	if len(request.Options.BodyBytes) > 0 {
		bodyReader = bytes.NewReader(request.Options.BodyBytes)
	} else {
		bodyReader = strings.NewReader(request.Options.Body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(request.Options.Method), request.Options.URL, bodyReader)
	if err != nil {
		log.Fatal(err)
	}
	headerorder := []string{}
	//master header order, all your headers will be ordered based on this list and anything extra will be appended to the end
	//if your site has any custom headers, see the header order chrome uses and then add those headers to this list
	if len(request.Options.HeaderOrder) > 0 {
		//lowercase headers
		for _, v := range request.Options.HeaderOrder {
			lowercasekey := strings.ToLower(v)
			headerorder = append(headerorder, lowercasekey)
		}
	} else {
		headerorder = append(headerorder,
			"host",
			"connection",
			"cache-control",
			"device-memory",
			"viewport-width",
			"rtt",
			"downlink",
			"ect",
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"sec-ch-ua-full-version",
			"sec-ch-ua-arch",
			"sec-ch-ua-platform",
			"sec-ch-ua-platform-version",
			"sec-ch-ua-model",
			"upgrade-insecure-requests",
			"user-agent",
			"accept",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-user",
			"sec-fetch-dest",
			"referer",
			"accept-encoding",
			"accept-language",
			"cookie",
		)
	}

	headermap := make(map[string]string)
	//TODO: Shorten this
	headerorderkey := []string{}
	for _, key := range headerorder {
		for k, v := range request.Options.Headers {
			lowercasekey := strings.ToLower(k)
			if key == lowercasekey {
				headermap[k] = v
				headerorderkey = append(headerorderkey, lowercasekey)
			}
		}

	}
	headerOrder := parseUserAgent(request.Options.UserAgent).HeaderOrder

	//ordering the pseudo headers and our normal headers
	req.Header = http.Header{
		http.HeaderOrderKey: headerorderkey,
	}
	// Only set PHeaderOrderKey for HTTP/2, not HTTP/3
	// HTTP/3 requests are handled by dispatchHTTP3Request() which doesn't reach this code
	if !request.Options.ForceHTTP3 && request.Options.Protocol != "http3" {
		req.Header[http.PHeaderOrderKey] = headerOrder
	}
	//set our Host header
	u, err := url.Parse(request.Options.URL)
	if err != nil {
		result.options = request
		result.err = fmt.Errorf("invalid URL: %w", err)
		return result
	}

	//append our normal headers
	for k, v := range request.Options.Headers {
		if k != "Content-Length" {
			req.Header.Set(k, v)
		}
	}

	// Respect user-provided Host header for domain fronting; otherwise default to URL host
	if _, ok := request.Options.Headers["Host"]; !ok {
		if _, ok := request.Options.Headers["host"]; !ok {
			req.Header.Set("Host", u.Host)
		}
	}
	req.Header.Set("user-agent", request.Options.UserAgent)

	state.RegisterRequest(request.RequestID, cancel)

	return fullRequest{req: req, client: client, options: request}
}

// dispatchHTTP3Request handles HTTP/3 specific request processing
func dispatchHTTP3Request(request cycleTLSRequest) (result fullRequest) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create browser configuration for HTTP/3 with forced settings
	browser := browserFromOptions(request.Options)
	browser.ForceHTTP1 = false
	browser.ForceHTTP3 = true

	// Connection reuse is enabled by default
	client, err := newClientWithReuse(
		browser,
		request.Options.Timeout,
		request.Options.DisableRedirect,
		request.Options.UserAgent,
		request.Options.EnableConnectionReuse != false,
		request.Options.Proxy,
	)
	if err != nil {
		log.Fatal(err)
	}

	// Handle both string body and byte body
	var bodyReader io.Reader
	if len(request.Options.BodyBytes) > 0 {
		bodyReader = bytes.NewReader(request.Options.BodyBytes)
	} else {
		bodyReader = strings.NewReader(request.Options.Body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(request.Options.Method), request.Options.URL, bodyReader)
	if err != nil {
		log.Fatal(err)
	}

	// Set headers for HTTP/3 request
	for k, v := range request.Options.Headers {
		if k != "Content-Length" {
			req.Header.Set(k, v)
		}
	}

	// Parse URL for Host header
	u, err := url.Parse(request.Options.URL)
	if err != nil {
		result.options = request
		result.err = fmt.Errorf("invalid URL: %w", err)
		return result
	}
	// Respect user-provided Host header for domain fronting; otherwise default to URL host
	if _, ok := request.Options.Headers["Host"]; !ok {
		if _, ok := request.Options.Headers["host"]; !ok {
			req.Header.Set("Host", u.Host)
		}
	}
	req.Header.Set("user-agent", request.Options.UserAgent)

	state.RegisterRequest(request.RequestID, cancel)

	return fullRequest{req: req, client: client, options: request}
}

// dispatchSSERequest handles SSE specific request processing
func dispatchSSERequest(request cycleTLSRequest) (result fullRequest) {
	ctx, cancel := context.WithCancel(context.Background())
	browser := browserFromOptions(request.Options)

	// Connection reuse is enabled by default
	client, err := newClientWithReuse(
		browser,
		request.Options.Timeout,
		request.Options.DisableRedirect,
		request.Options.UserAgent,
		request.Options.EnableConnectionReuse != false,
		request.Options.Proxy,
	)
	if err != nil {
		log.Fatal(err)
	}

	// Prepare headers for SSE
	headers := make(http.Header)
	for k, v := range request.Options.Headers {
		headers.Set(k, v)
	}

	// Create SSE client
	sseClient := NewSSEClient(&client, headers)

	// Create a placeholder request for consistency
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.Options.URL, nil)
	if err != nil {
		log.Fatal(err)
	}

	state.RegisterRequest(request.RequestID, cancel)

	return fullRequest{
		req:       req,
		client:    client,
		options:   request,
		sseClient: sseClient,
	}
}

// dispatchWebSocketRequest handles WebSocket specific request processing
func dispatchWebSocketRequest(request cycleTLSRequest) (result fullRequest) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create browser configuration for WebSocket (no HTTP/3 support)
	browser := browserFromOptions(request.Options)
	browser.ForceHTTP3 = false

	// Get TLS config for WebSocket
	tlsConfig := &utls.Config{
		InsecureSkipVerify: browser.InsecureSkipVerify,
		ServerName:         browser.ServerName,
	}

	// Prepare headers for WebSocket
	headers := make(http.Header)
	for k, v := range request.Options.Headers {
		headers.Set(k, v)
	}

	// Create WebSocket client
	convertedHeaders := ConvertFhttpHeader(headers)
	wsClient := NewWebSocketClient(tlsConfig, convertedHeaders)

	// Create a placeholder request for consistency
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.Options.URL, nil)
	if err != nil {
		log.Fatal(err)
	}

	state.RegisterRequest(request.RequestID, cancel)

	return fullRequest{
		req:      req,
		client:   http.Client{}, // Empty client as WebSocket uses its own dialer
		options:  request,
		wsClient: wsClient,
		ctx:      ctx,
		cancel:   cancel,
	}
}

func dispatcherAsync(res fullRequest, chanWrite *safeChannelWriter) {
	// Add panic recovery to prevent crashes from channel issues
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in dispatcherAsync for request %s: %v", res.options.RequestID, r)
		}
	}()

	// Check for early errors (URL parsing, etc.)
	if res.err != nil {
		b := getBuffer()
		defer putBuffer(b)
		var requestIDLength = len(res.options.RequestID)
		var statusCode = 400

		b.WriteByte(byte(requestIDLength >> 8))
		b.WriteByte(byte(requestIDLength))
		b.WriteString(res.options.RequestID)
		b.WriteByte(0)
		b.WriteByte(5)
		b.WriteString("error")
		b.WriteByte(byte(statusCode >> 8))
		b.WriteByte(byte(statusCode))

		message := res.err.Error()
		messageLength := len(message)

		b.WriteByte(byte(messageLength >> 8))
		b.WriteByte(byte(messageLength))
		b.WriteString(message)

		if !chanWrite.write(b.Bytes()) {
			log.Printf("Failed to write error response for request %s: channel closed", res.options.RequestID)
		}
		return
	}

	// Handle SSE connections
	if res.sseClient != nil {
		dispatchSSEAsync(res, chanWrite)
		return
	}

	// Handle WebSocket connections
	if res.wsClient != nil {
		dispatchWebSocketAsync(res, chanWrite)
		return
	}

	defer func() {
		state.UnregisterRequest(res.options.RequestID)
	}()

	// Extract host from URL for connection reuse tracking
	urlObj, _ := url.Parse(res.options.Options.URL)
	hostPort := urlObj.Host
	if !strings.Contains(hostPort, ":") {
		if urlObj.Scheme == "https" {
			hostPort = hostPort + ":443" // Default HTTPS port
		} else {
			hostPort = hostPort + ":80" // Default HTTP port
		}
	}

	// Don't close connections when finished - they'll be reused for the same host
	// Instead, tell the roundtripper to keep this connection but close others
	defer func() {
		// Use type assertion to access the roundTripper
		if transport, ok := res.client.Transport.(*roundTripper); ok {
			transport.CloseIdleConnections(hostPort)
		}
	}()

	finalUrl := res.options.Options.URL

	resp, err := res.client.Do(res.req)

	if err != nil {
		parsedError := parseError(err)

		{
			b := getBuffer()
			var requestIDLength = len(res.options.RequestID)

			b.WriteByte(byte(requestIDLength >> 8))
			b.WriteByte(byte(requestIDLength))
			b.WriteString(res.options.RequestID)
			b.WriteByte(0)
			b.WriteByte(5)
			b.WriteString("error")
			b.WriteByte(byte(parsedError.StatusCode >> 8))
			b.WriteByte(byte(parsedError.StatusCode))

			var message = parsedError.ErrorMsg + "-> \n" + string(err.Error())
			var messageLength = len(message)

			b.WriteByte(byte(messageLength >> 8))
			b.WriteByte(byte(messageLength))
			b.WriteString(message)

			if !chanWrite.write(b.Bytes()) {
				log.Printf("Failed to write error response for request %s: channel closed", res.options.RequestID)
			}
			putBuffer(b)
		}

		return
	}

	defer resp.Body.Close()

	// Update finalUrl if redirect occurred
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		finalUrl = resp.Request.URL.String()
	}

	{
		b := getBuffer()
		var headerLength = len(resp.Header)
		var requestIDLength = len(res.options.RequestID)
		var finalUrlLength = len(finalUrl)

		b.WriteByte(byte(requestIDLength >> 8))
		b.WriteByte(byte(requestIDLength))
		b.WriteString(res.options.RequestID)
		b.WriteByte(0)
		b.WriteByte(8)
		b.WriteString("response")
		b.WriteByte(byte(resp.StatusCode >> 8))
		b.WriteByte(byte(resp.StatusCode))

		// Write finalUrl length and value
		b.WriteByte(byte(finalUrlLength >> 8))
		b.WriteByte(byte(finalUrlLength))
		b.WriteString(finalUrl)

		// Write headers
		b.WriteByte(byte(headerLength >> 8))
		b.WriteByte(byte(headerLength))

		for name, values := range resp.Header {
			var nameLength = len(name)
			var valuesLength = len(values)

			b.WriteByte(byte(nameLength >> 8))
			b.WriteByte(byte(nameLength))
			b.WriteString(name)
			b.WriteByte(byte(valuesLength >> 8))
			b.WriteByte(byte(valuesLength))

			for _, value := range values {
				var valueLength = len(value)

				b.WriteByte(byte(valueLength >> 8))
				b.WriteByte(byte(valueLength))
				b.WriteString(value)
			}
		}

		if !chanWrite.write(b.Bytes()) {
			log.Printf("Failed to write to channel: channel closed")
			putBuffer(b)
			return
		}
		putBuffer(b)
	}

	{
		bufferSize := 8192
		chunkBuffer := make([]byte, bufferSize)

	loop:
		for {
			select {
			case <-res.req.Context().Done():
				debugLogger.Printf("Request %s was canceled during processing", res.options.RequestID)
				break loop

			default:
				n, err := resp.Body.Read(chunkBuffer)

				if res.req.Context().Err() != nil {
					debugLogger.Printf("Request %s was canceled during body read", res.options.RequestID)
					break loop
				}

				if err != nil && err != io.EOF {
					// Log to stdout instead of stderr to avoid process restart
					debugLogger.Printf("Read error: %s", err.Error())

					// Send error frame before breaking
					parsedError := parseError(err)
					b := getBuffer()
					requestIDLength := len(res.options.RequestID)

					b.WriteByte(byte(requestIDLength >> 8))
					b.WriteByte(byte(requestIDLength))
					b.WriteString(res.options.RequestID)
					b.WriteByte(0)
					b.WriteByte(5)
					b.WriteString("error")
					b.WriteByte(byte(parsedError.StatusCode >> 8))
					b.WriteByte(byte(parsedError.StatusCode))

					message := parsedError.ErrorMsg
					messageLength := len(message)

					b.WriteByte(byte(messageLength >> 8))
					b.WriteByte(byte(messageLength))
					b.WriteString(message)

					if !chanWrite.write(b.Bytes()) {
						log.Printf("Failed to write to channel: channel closed")
						putBuffer(b)
						return
					}
					putBuffer(b)
					break loop
				}

				if err == io.EOF {
					// Handle any remaining data first
					if n > 0 {
						b := getBuffer()
						requestIDLength := len(res.options.RequestID)
						bodyChunkLength := n

						b.WriteByte(byte(requestIDLength >> 8))
						b.WriteByte(byte(requestIDLength))
						b.WriteString(res.options.RequestID)
						b.WriteByte(0)
						b.WriteByte(4)
						b.WriteString("data")
						b.WriteByte(byte(bodyChunkLength >> 24))
						b.WriteByte(byte(bodyChunkLength >> 16))
						b.WriteByte(byte(bodyChunkLength >> 8))
						b.WriteByte(byte(bodyChunkLength))
						b.Write(chunkBuffer[:n])

						if !chanWrite.write(b.Bytes()) {
							log.Printf("Failed to write to channel: channel closed")
							putBuffer(b)
							return
						}
						putBuffer(b)
					}
					// EOF reached, exit the loop
					break loop
				}

				if n == 0 {
					// No data available right now, continue reading (don't break)
					continue
				}

				b := getBuffer()
				requestIDLength := len(res.options.RequestID)
				bodyChunkLength := n

				b.WriteByte(byte(requestIDLength >> 8))
				b.WriteByte(byte(requestIDLength))
				b.WriteString(res.options.RequestID)
				b.WriteByte(0)
				b.WriteByte(4)
				b.WriteString("data")
				b.WriteByte(byte(bodyChunkLength >> 24))
				b.WriteByte(byte(bodyChunkLength >> 16))
				b.WriteByte(byte(bodyChunkLength >> 8))
				b.WriteByte(byte(bodyChunkLength))
				b.Write(chunkBuffer[:n])

				if !chanWrite.write(b.Bytes()) {
					log.Printf("Failed to write to channel: channel closed")
					putBuffer(b)
					return
				}
				putBuffer(b)
			}
		}
	}

	{
		b := getBuffer()
		requestIDLength := len(res.options.RequestID)

		b.WriteByte(byte(requestIDLength >> 8))
		b.WriteByte(byte(requestIDLength))
		b.WriteString(res.options.RequestID)
		b.WriteByte(0)
		b.WriteByte(3)
		b.WriteString("end")

		if !chanWrite.write(b.Bytes()) {
			log.Printf("Failed to write to channel: channel closed")
			putBuffer(b)
			return
		}
		putBuffer(b)
	}
}

// dispatchSSEAsync handles SSE connections asynchronously
func dispatchSSEAsync(res fullRequest, chanWrite *safeChannelWriter) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in dispatchSSEAsync for request %s: %v", res.options.RequestID, r)
		}
		state.UnregisterRequest(res.options.RequestID)
	}()

	// Connect to SSE endpoint
	sseResp, err := res.sseClient.Connect(res.req.Context(), res.options.Options.URL)
	if err != nil {
		// Send error response
		b := getBuffer()
		var requestIDLength = len(res.options.RequestID)

		b.WriteByte(byte(requestIDLength >> 8))
		b.WriteByte(byte(requestIDLength))
		b.WriteString(res.options.RequestID)
		b.WriteByte(0)
		b.WriteByte(5)
		b.WriteString("error")
		b.WriteByte(byte(0 >> 8)) // Status code 0
		b.WriteByte(byte(0))

		var message = "SSE connection failed: " + err.Error()
		var messageLength = len(message)

		b.WriteByte(byte(messageLength >> 8))
		b.WriteByte(byte(messageLength))
		b.WriteString(message)

		if !chanWrite.write(b.Bytes()) {
			log.Printf("Failed to write to channel: channel closed")
			putBuffer(b)
			return
		}
		putBuffer(b)
		return
	}
	defer sseResp.Close()

	// Send initial response with headers
	{
		b := getBuffer()
		var headerLength = len(sseResp.Response.Header)
		var requestIDLength = len(res.options.RequestID)
		var finalUrlLength = len(res.options.Options.URL)

		b.WriteByte(byte(requestIDLength >> 8))
		b.WriteByte(byte(requestIDLength))
		b.WriteString(res.options.RequestID)
		b.WriteByte(0)
		b.WriteByte(8)
		b.WriteString("response")
		b.WriteByte(byte(sseResp.Response.StatusCode >> 8))
		b.WriteByte(byte(sseResp.Response.StatusCode))

		// Write finalUrl length and value
		b.WriteByte(byte(finalUrlLength >> 8))
		b.WriteByte(byte(finalUrlLength))
		b.WriteString(res.options.Options.URL)

		// Write headers
		b.WriteByte(byte(headerLength >> 8))
		b.WriteByte(byte(headerLength))

		for name, values := range sseResp.Response.Header {
			var nameLength = len(name)
			var valuesLength = len(values)

			b.WriteByte(byte(nameLength >> 8))
			b.WriteByte(byte(nameLength))
			b.WriteString(name)
			b.WriteByte(byte(valuesLength >> 8))
			b.WriteByte(byte(valuesLength))

			for _, value := range values {
				var valueLength = len(value)

				b.WriteByte(byte(valueLength >> 8))
				b.WriteByte(byte(valueLength))
				b.WriteString(value)
			}
		}

		if !chanWrite.write(b.Bytes()) {
			log.Printf("Failed to write to channel: channel closed")
			putBuffer(b)
			return
		}
		putBuffer(b)
	}

	// Read SSE events
	for {
		select {
		case <-res.req.Context().Done():
			debugLogger.Printf("SSE request %s was canceled", res.options.RequestID)
			break

		default:
			event, err := sseResp.NextEvent()
			if err != nil {
				if err == io.EOF {
					// Normal end of stream
					break
				}
				debugLogger.Printf("SSE read error: %s", err.Error())
				break
			}

			if event == nil {
				continue
			}

			// Format SSE event as JSON for transmission
			eventData := map[string]interface{}{
				"event": event.Event,
				"data":  event.Data,
				"id":    event.ID,
				"retry": event.Retry,
			}

			eventBytes, err := json.Marshal(eventData)
			if err != nil {
				debugLogger.Printf("SSE event marshal error: %s", err.Error())
				continue
			}

			// Send event data
			b := getBuffer()
			requestIDLength := len(res.options.RequestID)
			bodyChunkLength := len(eventBytes)

			b.WriteByte(byte(requestIDLength >> 8))
			b.WriteByte(byte(requestIDLength))
			b.WriteString(res.options.RequestID)
			b.WriteByte(0)
			b.WriteByte(4)
			b.WriteString("data")
			b.WriteByte(byte(bodyChunkLength >> 24))
			b.WriteByte(byte(bodyChunkLength >> 16))
			b.WriteByte(byte(bodyChunkLength >> 8))
			b.WriteByte(byte(bodyChunkLength))
			b.Write(eventBytes)

			if !chanWrite.write(b.Bytes()) {
				log.Printf("Failed to write to channel: channel closed")
				putBuffer(b)
				return
			}
			putBuffer(b)
		}
	}

	// Send end message
	{
		b := getBuffer()
		requestIDLength := len(res.options.RequestID)

		b.WriteByte(byte(requestIDLength >> 8))
		b.WriteByte(byte(requestIDLength))
		b.WriteString(res.options.RequestID)
		b.WriteByte(0)
		b.WriteByte(3)
		b.WriteString("end")

		if !chanWrite.write(b.Bytes()) {
			log.Printf("Failed to write to channel: channel closed")
			putBuffer(b)
			return
		}
		putBuffer(b)
	}
}

// dispatchWebSocketAsync handles WebSocket connections asynchronously with full bidirectional support
func dispatchWebSocketAsync(res fullRequest, chanWrite *safeChannelWriter) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in dispatchWebSocketAsync for request %s: %v", res.options.RequestID, r)
		}
		state.UnregisterRequest(res.options.RequestID)

		// Remove from active WebSockets
		state.UnregisterWebSocket(res.options.RequestID)
	}()

	// Connect to WebSocket endpoint
	conn, resp, err := res.wsClient.Connect(res.options.Options.URL)
	if err != nil {
		sendWebSocketError(chanWrite, res.options.RequestID, res.options.Options.URL, resp, err)
		return
	}
	defer conn.Close()

	// Extract negotiated protocol and extensions
	negotiatedProtocol := resp.Header.Get("Sec-WebSocket-Protocol")
	negotiatedExtensions := resp.Header.Get("Sec-WebSocket-Extensions")

	// Create WebSocket connection object with done channel for goroutine cleanup
	wsConn := &WebSocketConnection{
		Conn:        conn,
		RequestID:   res.options.RequestID,
		URL:         res.options.Options.URL,
		ReadyState:  1, // OPEN
		commandChan: make(chan WebSocketCommand, 100),
		closeChan:   make(chan struct{}),
		done:        make(chan struct{}), // signals all goroutines to exit
		chanWrite:   chanWrite,
		protocol:    negotiatedProtocol,
		extensions:  negotiatedExtensions,
	}

	// Register the WebSocket connection
	state.RegisterWebSocket(res.options.RequestID, wsConn)

	// Send initial response with headers
	sendWebSocketResponse(chanWrite, res.options.RequestID, res.options.Options.URL, resp)

	// Send ws_open event
	sendWebSocketOpen(chanWrite, res.options.RequestID, negotiatedProtocol, negotiatedExtensions)

	// If there's body data, send it as the first WebSocket message
	if res.options.Options.Body != "" {
		err := wsConn.safeWrite(conn, websocket.TextMessage, []byte(res.options.Options.Body))
		if err != nil {
			debugLogger.Printf("WebSocket write error: %s", err.Error())
		}
	}

	// Read deadline timeout - prevents goroutine from blocking forever on reads
	const wsReadDeadline = 30 * time.Second

	// Start goroutines with WaitGroup tracking
	wsConn.wg.Add(2)

	// Goroutine to handle incoming WebSocket messages
	go func() {
		defer wsConn.wg.Done()
		for {
			// Check if we should exit before blocking on read
			select {
			case <-wsConn.done:
				return
			default:
			}

			// Set read deadline to prevent blocking forever
			conn.SetReadDeadline(time.Now().Add(wsReadDeadline))

			messageType, message, err := conn.ReadMessage()
			if err != nil {
				// Check if this is a timeout - if so, just continue the loop
				if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
					// Check if we should exit
					select {
					case <-wsConn.done:
						return
					default:
						continue // Just a timeout, keep reading
					}
				}

				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					// Normal close
					sendWebSocketClose(chanWrite, res.options.RequestID, websocket.CloseNormalClosure, "Connection closed normally")
				} else {
					debugLogger.Printf("WebSocket read error: %s", err.Error())
					sendWebSocketError(chanWrite, res.options.RequestID, res.options.Options.URL, nil, err)
				}
				return
			}

			// Send ws_message event
			sendWebSocketMessage(chanWrite, res.options.RequestID, messageType, message)
		}
	}()

	// Goroutine to handle outgoing commands
	go func() {
		defer wsConn.wg.Done()
		for {
			select {
			case <-wsConn.done:
				return

			case cmd := <-wsConn.commandChan:
				switch cmd.Type {
				case "send":
					msgType := websocket.TextMessage
					if cmd.IsBinary {
						msgType = websocket.BinaryMessage
					}
					err := wsConn.safeWrite(conn, msgType, cmd.Data)
					if err != nil {
						debugLogger.Printf("WebSocket send error: %s", err.Error())
						sendWebSocketError(chanWrite, res.options.RequestID, res.options.Options.URL, nil, err)
					}

				case "close":
					wsConn.mu.Lock()
					wsConn.ReadyState = 2 // CLOSING
					wsConn.mu.Unlock()

					closeCode := cmd.CloseCode
					if closeCode == 0 {
						closeCode = websocket.CloseNormalClosure
					}

					err := wsConn.safeWrite(conn, websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, cmd.CloseReason))
					if err != nil {
						debugLogger.Printf("WebSocket close error: %s", err.Error())
					}

					sendWebSocketClose(chanWrite, res.options.RequestID, closeCode, cmd.CloseReason)
					return

				case "ping":
					err := wsConn.safeWrite(conn, websocket.PingMessage, cmd.Data)
					if err != nil {
						debugLogger.Printf("WebSocket ping error: %s", err.Error())
					}

				case "pong":
					err := wsConn.safeWrite(conn, websocket.PongMessage, cmd.Data)
					if err != nil {
						debugLogger.Printf("WebSocket pong error: %s", err.Error())
					}
				}

			case <-wsConn.closeChan:
				return

			case <-res.req.Context().Done():
				debugLogger.Printf("WebSocket request %s was canceled", res.options.RequestID)
				return
			}
		}
	}()

	// Wait for context cancellation or close signal
	select {
	case <-wsConn.closeChan:
		// Connection close requested
	case <-res.req.Context().Done():
		debugLogger.Printf("WebSocket request %s was canceled", res.options.RequestID)
	}

	// Signal all goroutines to exit and wait for them
	close(wsConn.done)
	wsConn.wg.Wait()

	// Update connection state to CLOSED
	wsConn.mu.Lock()
	wsConn.ReadyState = 3
	wsConn.mu.Unlock()

	// Send end message
	sendWebSocketEnd(chanWrite, res.options.RequestID)
}

// Helper functions for sending WebSocket messages
func sendWebSocketError(chanWrite *safeChannelWriter, requestID, url string, resp *nhttp.Response, err error) {
	b := getBuffer()
	defer putBuffer(b)
	var requestIDLength = len(requestID)

	b.WriteByte(byte(requestIDLength >> 8))
	b.WriteByte(byte(requestIDLength))
	b.WriteString(requestID)
	b.WriteByte(0)
	b.WriteByte(8)
	b.WriteString("ws_error")

	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}

	b.WriteByte(byte(statusCode >> 8))
	b.WriteByte(byte(statusCode))

	var message = err.Error()
	var messageLength = len(message)

	b.WriteByte(byte(messageLength >> 8))
	b.WriteByte(byte(messageLength))
	b.WriteString(message)

	chanWrite.write(b.Bytes())
}

func sendWebSocketResponse(chanWrite *safeChannelWriter, requestID, url string, resp *nhttp.Response) {
	b := getBuffer()
	defer putBuffer(b)
	var headerLength = len(resp.Header)
	var requestIDLength = len(requestID)
	var finalUrlLength = len(url)

	b.WriteByte(byte(requestIDLength >> 8))
	b.WriteByte(byte(requestIDLength))
	b.WriteString(requestID)
	b.WriteByte(0)
	b.WriteByte(8)
	b.WriteString("response")
	b.WriteByte(byte(resp.StatusCode >> 8))
	b.WriteByte(byte(resp.StatusCode))

	// Write finalUrl length and value
	b.WriteByte(byte(finalUrlLength >> 8))
	b.WriteByte(byte(finalUrlLength))
	b.WriteString(url)

	// Write headers
	b.WriteByte(byte(headerLength >> 8))
	b.WriteByte(byte(headerLength))

	for name, values := range resp.Header {
		var nameLength = len(name)
		var valuesLength = len(values)

		b.WriteByte(byte(nameLength >> 8))
		b.WriteByte(byte(nameLength))
		b.WriteString(name)
		b.WriteByte(byte(valuesLength >> 8))
		b.WriteByte(byte(valuesLength))

		for _, value := range values {
			var valueLength = len(value)

			b.WriteByte(byte(valueLength >> 8))
			b.WriteByte(byte(valueLength))
			b.WriteString(value)
		}
	}

	chanWrite.write(b.Bytes())
}

func sendWebSocketOpen(chanWrite *safeChannelWriter, requestID, protocol, extensions string) {
	openMsg := map[string]interface{}{
		"type":       "open",
		"protocol":   protocol,
		"extensions": extensions,
	}

	msgBytes, _ := json.Marshal(openMsg)

	b := getBuffer()
	defer putBuffer(b)
	requestIDLength := len(requestID)
	bodyChunkLength := len(msgBytes)

	b.WriteByte(byte(requestIDLength >> 8))
	b.WriteByte(byte(requestIDLength))
	b.WriteString(requestID)
	b.WriteByte(0)
	b.WriteByte(7)
	b.WriteString("ws_open")
	b.WriteByte(byte(bodyChunkLength >> 24))
	b.WriteByte(byte(bodyChunkLength >> 16))
	b.WriteByte(byte(bodyChunkLength >> 8))
	b.WriteByte(byte(bodyChunkLength))
	b.Write(msgBytes)

	chanWrite.write(b.Bytes())
}

func sendWebSocketMessage(chanWrite *safeChannelWriter, requestID string, messageType int, message []byte) {
	b := getBuffer()
	defer putBuffer(b)
	requestIDLength := len(requestID)

	b.WriteByte(byte(requestIDLength >> 8))
	b.WriteByte(byte(requestIDLength))
	b.WriteString(requestID)
	b.WriteByte(0)
	b.WriteByte(10)
	b.WriteString("ws_message")

	// Message type (1 byte)
	b.WriteByte(byte(messageType))

	// Message data length (4 bytes)
	messageLength := len(message)
	b.WriteByte(byte(messageLength >> 24))
	b.WriteByte(byte(messageLength >> 16))
	b.WriteByte(byte(messageLength >> 8))
	b.WriteByte(byte(messageLength))

	// Message data
	b.Write(message)

	chanWrite.write(b.Bytes())
}

func sendWebSocketClose(chanWrite *safeChannelWriter, requestID string, code int, reason string) {
	closeMsg := map[string]interface{}{
		"type":   "close",
		"code":   code,
		"reason": reason,
	}

	msgBytes, _ := json.Marshal(closeMsg)

	b := getBuffer()
	defer putBuffer(b)
	requestIDLength := len(requestID)
	bodyChunkLength := len(msgBytes)

	b.WriteByte(byte(requestIDLength >> 8))
	b.WriteByte(byte(requestIDLength))
	b.WriteString(requestID)
	b.WriteByte(0)
	b.WriteByte(8)
	b.WriteString("ws_close")
	b.WriteByte(byte(bodyChunkLength >> 24))
	b.WriteByte(byte(bodyChunkLength >> 16))
	b.WriteByte(byte(bodyChunkLength >> 8))
	b.WriteByte(byte(bodyChunkLength))
	b.Write(msgBytes)

	chanWrite.write(b.Bytes())
}

func sendWebSocketEnd(chanWrite *safeChannelWriter, requestID string) {
	b := getBuffer()
	defer putBuffer(b)
	requestIDLength := len(requestID)

	b.WriteByte(byte(requestIDLength >> 8))
	b.WriteByte(byte(requestIDLength))
	b.WriteString(requestID)
	b.WriteByte(0)
	b.WriteByte(3)
	b.WriteString("end")

	chanWrite.write(b.Bytes())
}

func writeSocket(chanWrite chan []byte, wsSocket *websocket.Conn) {
	for buf := range chanWrite {
		err := wsSocket.WriteMessage(websocket.BinaryMessage, buf)

		if err != nil {
			log.Print("Socket WriteMessage Failed" + err.Error())
			continue
		}
	}
}

func readSocket(chanRead chan fullRequest, wsSocket *websocket.Conn) {
	for {
		_, message, err := wsSocket.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				return
			}
			log.Print("Socket Error", err)
			return
		}
		var baseMessage map[string]interface{}
		if err := json.Unmarshal(message, &baseMessage); err != nil {
			log.Print("Unmarshal Error", err)
			return
		}
		if action, ok := baseMessage["action"]; ok {
			if action == "exit" {
				// Respond by sending a close frame and then close the connection.
				wsSocket.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "exit"))
				wsSocket.Close()
				return
			}
			if action == "cancel" {
				requestId, _ := baseMessage["requestId"].(string)
				state.CancelRequest(requestId)
				continue
			}
			// Handle WebSocket commands
			if action == "ws_send" || action == "ws_close" || action == "ws_ping" || action == "ws_pong" {
				requestId, _ := baseMessage["requestId"].(string)

				wsConnInterface, exists := state.GetWebSocket(requestId)
				if !exists {
					log.Printf("WebSocket connection not found for request ID: %s", requestId)
					continue
				}
				wsConn := wsConnInterface.(*WebSocketConnection)

				cmd := WebSocketCommand{}

				switch action {
				case "ws_send":
					cmd.Type = "send"
					if dataStr, ok := baseMessage["data"].(string); ok {
						cmd.Data = []byte(dataStr)
					}
					if isBinary, ok := baseMessage["isBinary"].(bool); ok {
						cmd.IsBinary = isBinary
					}

				case "ws_close":
					cmd.Type = "close"
					if code, ok := baseMessage["code"].(float64); ok {
						cmd.CloseCode = int(code)
					}
					if reason, ok := baseMessage["reason"].(string); ok {
						cmd.CloseReason = reason
					}

				case "ws_ping":
					cmd.Type = "ping"
					if dataStr, ok := baseMessage["data"].(string); ok {
						cmd.Data = []byte(dataStr)
					}

				case "ws_pong":
					cmd.Type = "pong"
					if dataStr, ok := baseMessage["data"].(string); ok {
						cmd.Data = []byte(dataStr)
					}
				}

				// Send command to WebSocket connection
				select {
				case wsConn.commandChan <- cmd:
					// Command sent successfully
				default:
					log.Printf("WebSocket command channel full for request ID: %s", requestId)
				}

				continue
			}
		}
		// (If there was no "action" field, process as usual)
		request := new(cycleTLSRequest)
		if err := json.Unmarshal(message, &request); err != nil {
			log.Print("Unmarshal Error", err)
			return
		}
		chanRead <- processRequest(*request)
	}
}

// Worker
func readProcess(chanRead chan fullRequest, chanWrite *safeChannelWriter) {
	for request := range chanRead {
		go dispatcherAsync(request, chanWrite)
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  65536, // 64KB buffer for large init packets
	WriteBufferSize: 65536, // 64KB buffer for large responses
}

// WSEndpoint exports the main cycletls function as we websocket connection that clients can connect to
func WSEndpoint(w nhttp.ResponseWriter, r *nhttp.Request) {
	upgrader.CheckOrigin = func(r *nhttp.Request) bool { return true }

	// upgrade this connection to a WebSocket
	// connection
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		//Golang Received a non-standard request to this port, printing request
		var data map[string]interface{}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Print("Invalid Request: Body Read Error" + err.Error())
		}
		err = json.Unmarshal(bodyBytes, &data)
		if err != nil {
			log.Print("Invalid Request: Json Conversion failed ")
		}
		body, err := PrettyStruct(data)
		if err != nil {
			log.Print("Invalid Request:", err)
		}
		headers, err := PrettyStruct(r.Header)
		if err != nil {
			log.Fatal(err)
		}
		log.Println(headers)
		log.Println(body)

	} else {
		// Version routing: v=2 is now default, v=1 for legacy compatibility
		version := r.URL.Query().Get("v")
		if version == "1" {
			// V1 (legacy): Multiplexed JSON protocol - use ?v=1 for backward compat
			goto legacyHandler
		}

		// V2 (default): One WebSocket per request with flow control
		handleWSRequestV2(ws)
		return

	legacyHandler:
		// Legacy multiplexed JSON protocol
		chanRead := make(chan fullRequest)
		chanWrite := make(chan []byte)
		safeWriter := newSafeChannelWriter(chanWrite)

		go readSocket(chanRead, ws)
		go readProcess(chanRead, safeWriter)

		// Run as main thread - when this exits, mark channel as closed
		writeSocket(chanWrite, ws)
		safeWriter.setClosed()
	}
}

func setupRoutes() {
	nhttp.HandleFunc("/", WSEndpoint)
}

func main() {
	port, exists := os.LookupEnv("WS_PORT")
	var addr *string
	if exists {
		addr = flag.String("addr", ":"+port, "http service address")
	} else {
		addr = flag.String("addr", ":9112", "http service address")
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	setupRoutes()
	log.Fatal(nhttp.ListenAndServe(*addr, nil))
}

// Response type moved to types.go

// Init creates a CycleTLS client with v1 default behavior (chan Response)
// Use WithRawBytes() option for performance enhancement with chan []byte
func Init(opts ...Option) CycleTLS {
	reqChan := make(chan fullRequest, 100)
	respChan := make(chan Response, 100)

	client := CycleTLS{
		ReqChan:  reqChan,
		RespChan: respChan,
	}

	// Apply options
	for _, opt := range opts {
		opt(&client)
	}

	return client
}

// Queue queues a request (simplified for integration tests)
func (client CycleTLS) Queue(URL string, options Options, Method string) {
	// This is a simplified implementation for integration tests
	// In a real implementation, this would queue the request
}

// Close closes the channels
func (client CycleTLS) Close() {
	if client.ReqChan != nil {
		close(client.ReqChan)
	}
	if client.RespChan != nil {
		close(client.RespChan)
	}
	if client.RespChanV2 != nil {
		close(client.RespChanV2)
	}
	// Clear all connections from the global pool
	clearAllConnections()
}

// Do creates a single HTTP request for integration tests
func (client CycleTLS) Do(URL string, options Options, Method string) (Response, error) {
	// Create browser from options
	browser := Browser{
		JA3:                options.Ja3,
		JA4r:               options.Ja4r,
		HTTP2Fingerprint:   options.HTTP2Fingerprint,
		QUICFingerprint:    options.QUICFingerprint,
		UserAgent:          options.UserAgent,
		Cookies:            options.Cookies,
		InsecureSkipVerify: options.InsecureSkipVerify,
		ForceHTTP1:         options.ForceHTTP1,
		ForceHTTP3:         options.ForceHTTP3,
		HeaderOrder:        options.HeaderOrder,
	}

	// Note: Don't automatically set HeaderOrder from UserAgent here as it can interfere with connection management
	// The pseudo-header order should be set through explicit HTTP2Fingerprint or Options.HeaderOrder

	// Create HTTP client with connection reuse enabled by default
	httpClient, err := newClientWithReuse(
		browser,
		options.Timeout,
		options.DisableRedirect,
		options.UserAgent,
		options.EnableConnectionReuse != false,
		options.Proxy,
	)
	if err != nil {
		return Response{}, err
	}

	// Create request using fhttp
	var bodyReader io.Reader
	if len(options.BodyBytes) > 0 {
		bodyReader = bytes.NewReader(options.BodyBytes)
	} else {
		bodyReader = strings.NewReader(options.Body)
	}
	req, err := http.NewRequest(Method, URL, bodyReader)
	if err != nil {
		return Response{}, err
	}

	// Set pseudo-header order based on UserAgent - only for HTTP/2, not HTTP/3
	headerOrder := parseUserAgent(options.UserAgent).HeaderOrder
	req.Header = http.Header{}

	// Only set PHeaderOrderKey for HTTP/2, not HTTP/3
	if !options.ForceHTTP3 {
		req.Header[http.PHeaderOrderKey] = headerOrder
	}

	// Set headers
	for k, v := range options.Headers {
		req.Header.Set(k, v)
	}

	// Make request
	resp, err := httpClient.Do(req)
	if err != nil {
		parsedError := parseError(err)
		return Response{
			Status: parsedError.StatusCode,
			Body:   parsedError.ErrorMsg + " -> " + err.Error(),
		}, nil
	}
	defer resp.Body.Close()

	// Read body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}

	// Automatic decompression (axios-style) - check Content-Encoding header
	encoding := resp.Header["Content-Encoding"]
	content := resp.Header["Content-Type"]
	if len(encoding) > 0 {
		// Automatically decompress the body like axios does
		bodyBytes = DecompressBody(bodyBytes, encoding, content)
	}

	// Convert headers
	headers := make(map[string]string)
	for name, values := range resp.Header {
		if len(values) > 0 {
			headers[name] = values[0]
		}
	}

	// Get final URL
	finalUrl := URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalUrl = resp.Request.URL.String()
	}

	// Convert fhttp cookies to net/http cookies
	var netCookies []*nhttp.Cookie
	for _, cookie := range resp.Cookies() {
		netCookie := &nhttp.Cookie{
			Name:       cookie.Name,
			Value:      cookie.Value,
			Path:       cookie.Path,
			Domain:     cookie.Domain,
			Expires:    cookie.Expires,
			RawExpires: cookie.RawExpires,
			MaxAge:     cookie.MaxAge,
			Secure:     cookie.Secure,
			HttpOnly:   cookie.HttpOnly,
			SameSite:   nhttp.SameSite(cookie.SameSite),
			Raw:        cookie.Raw,
			Unparsed:   cookie.Unparsed,
		}
		netCookies = append(netCookies, netCookie)
	}

	return Response{
		Status:    resp.StatusCode,
		Body:      string(bodyBytes),
		BodyBytes: bodyBytes, // Provide raw bytes for binary data
		Headers:   headers,
		Cookies:   netCookies,
		FinalUrl:  finalUrl,
	}, nil
}
