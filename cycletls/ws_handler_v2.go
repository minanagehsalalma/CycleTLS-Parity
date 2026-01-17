package cycletls

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
)

const (
	// pongWait is the time allowed to read the next pong message.
	pongWait = 30 * time.Second
	// pingPeriod is the interval between ping messages (before pong expiration).
	pingPeriod = (pongWait * 9) / 10
	// writeWait is the time allowed for writing a message.
	writeWait = 10 * time.Second
	// requestTimeout is the maximum duration for a single request.
	requestTimeout = 2 * time.Hour
)

// -----------------------------------------------------------------------------
// WebSocket abstraction for v2 flow control
// -----------------------------------------------------------------------------

// wsConnV2 abstracts WebSocket operations for testing and flexibility.
type wsConnV2 interface {
	ReadMessage() (int, []byte, error)
	WriteBinary([]byte) error
	Close() error
	SetCloseHandler(func(code int, text string) error)
}

// gorillaConnV2 wraps gorilla/websocket.Conn to implement wsConnV2.
type gorillaConnV2 struct {
	*websocket.Conn
}

// WriteBinary writes a binary message with proper deadline.
func (g gorillaConnV2) WriteBinary(data []byte) error {
	_ = g.Conn.SetWriteDeadline(time.Now().Add(writeWait))
	return g.Conn.WriteMessage(websocket.BinaryMessage, data)
}

// newGorillaConnV2 creates a new wrapped connection with pong handler.
func newGorillaConnV2(ws *websocket.Conn) wsConnV2 {
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))

	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	return gorillaConnV2{Conn: ws}
}

// -----------------------------------------------------------------------------
// V2 packet reading helpers
// -----------------------------------------------------------------------------

// readInitPacketV2 reads the first "init" packet sent by the client.
// It initializes the request and returns the initial credit window.
func readInitPacketV2(conn wsConnV2) (cycleTLSRequest, uint32, error) {
	msgType, payload, err := conn.ReadMessage()
	if err != nil {
		return cycleTLSRequest{}, 0, err
	}

	if msgType != websocket.BinaryMessage {
		return cycleTLSRequest{}, 0, fmt.Errorf("expected binary init packet")
	}

	return parseInitMessage(payload)
}

// readCreditPacketV2 reads "credit" packets sent by the client to replenish the window.
func readCreditPacketV2(conn wsConnV2, expectedRequestID string) (uint32, error) {
	msgType, payload, err := conn.ReadMessage()
	if err != nil {
		return 0, err
	}

	if msgType != websocket.BinaryMessage {
		return 0, fmt.Errorf("expected binary credit packet")
	}

	requestID, credits, err := parseCreditMessage(payload)
	if err != nil {
		return 0, err
	}

	if requestID != expectedRequestID {
		return 0, fmt.Errorf("unexpected request id %q", requestID)
	}

	return credits, nil
}

// -----------------------------------------------------------------------------
// V2 WebSocket request handler (one connection per request)
// -----------------------------------------------------------------------------

// handleWSRequestV2 handles a single request using the v2 flow-control protocol.
// This is the new handler for ?v=2 connections.
func handleWSRequestV2(ws *websocket.Conn) {
	conn := newGorillaConnV2(ws)

	// Root context for the request with timeout
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	// errgroup allows:
	// - waiting for all goroutines
	// - propagating the first error
	g, ctx := errgroup.WithContext(ctx)

	// Write channel to WebSocket.
	// IMPORTANT: the dispatcher is the ONLY owner of closing this channel.
	writeCh := make(chan []byte, 32)

	var limiter *creditWindow
	var once sync.Once

	// cleanup definitively closes the connection.
	// It is safe to call multiple times.
	cleanup := func() {
		once.Do(func() {
			if limiter != nil {
				limiter.Close()
			}
			_ = conn.Close()
		})
	}

	defer cleanup()

	conn.SetCloseHandler(func(code int, text string) error {
		debugLogger.Println("websocket closed by peer", code, text)
		cancel()
		return nil
	})

	// -----------------------------------------------------------------
	// Ping goroutine and write
	// -----------------------------------------------------------------

	controlCh := make(chan struct{}, 1)

	// ping goroutine - sends periodic keepalive pings
	g.Go(func() error {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				select {
				case controlCh <- struct{}{}:
				default:
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	// -----------------------------------------------------------------
	// Init handshake
	// -----------------------------------------------------------------

	req, initialWindow, err := readInitPacketV2(conn)
	if err != nil {
		debugLogger.Print("init error: ", err)
		return
	}

	// Process the request with parent context
	res := processRequestV2(req, ctx)

	limiter = newCreditWindow(int64(initialWindow))
	res.limiter = limiter

	// -----------------------------------------------------------------
	// Writer goroutine (exclusive writer to the WebSocket)
	// -----------------------------------------------------------------

	g.Go(func() error {
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case _, ok := <-controlCh:
				if !ok {
					return nil
				}
				err := ws.WriteControl(
					websocket.PingMessage,
					nil,
					time.Now().Add(writeWait),
				)
				if err != nil {
					return err
				}
			case buf, ok := <-writeCh:
				if !ok {
					return nil
				}
				err := conn.WriteBinary(buf)
				if err != nil {
					return err
				}
			}
		}
	})

	// -----------------------------------------------------------------
	// Credit reader loop
	// -----------------------------------------------------------------

	g.Go(func() error {
		defer cancel()

		for {
			credits, err := readCreditPacketV2(conn, req.RequestID)
			if err != nil {
				return err
			}
			limiter.Add(int64(credits))
		}
	})

	// -----------------------------------------------------------------
	// Dispatcher
	// -----------------------------------------------------------------
	//
	// The dispatcher:
	// - produces frames
	// - writes to writeCh
	// - CLOSES writeCh when finished
	//

	g.Go(func() error {
		defer close(writeCh)

		sender := newFrameSender(ctx, writeCh)
		dispatcherAsyncV2(res, sender)

		// IMPORTANT:
		// At this point:
		// - no more messages will be produced
		// - the writer will drain writeCh then stop
		return nil
	})

	// -----------------------------------------------------------------
	// Final wait
	// -----------------------------------------------------------------

	if err := g.Wait(); err != nil {
		debugLogger.Println("request error:", err)
	}
}

// processRequestV2 processes a request for v2 flow control mode.
// It uses the parent context for cancellation.
func processRequestV2(request cycleTLSRequest, parentCtx context.Context) fullRequest {
	// Handle protocol-specific clients
	switch request.Options.Protocol {
	case "websocket":
		return dispatchWebSocketRequest(request)
	case "sse":
		return dispatchSSERequest(request)
	case "http3":
		return dispatchHTTP3Request(request)
	default:
		if request.Options.ForceHTTP3 {
			return dispatchHTTP3Request(request)
		}
		return dispatchHTTPRequestV2(request, parentCtx)
	}
}

// dispatchHTTPRequestV2 creates a request for v2 flow control mode.
func dispatchHTTPRequestV2(request cycleTLSRequest, parentCtx context.Context) fullRequest {
	// Use the parent context for cancellation
	ctx, cancel := context.WithCancel(parentCtx)

	// Create browser config
	browser := browserFromOptions(request.Options)

	// Default to true for connection reuse
	enableConnectionReuse := request.Options.EnableConnectionReuse != false

	client, err := newClientWithReuse(
		browser,
		request.Options.Timeout,
		request.Options.DisableRedirect,
		request.Options.UserAgent,
		enableConnectionReuse,
		request.Options.Proxy,
	)
	if err != nil {
		log.Fatal(err)
	}

	// Build the HTTP request using the context-aware approach from the existing code
	req, err := buildHTTPRequest(request, ctx)
	if err != nil {
		return fullRequest{
			options: request,
			err:     err,
		}
	}

	return fullRequest{
		req:     req,
		client:  client,
		options: request,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// dispatcherAsyncV2 handles the request using flow control.
func dispatcherAsyncV2(res fullRequest, sender *frameSender) {
	limiter := res.limiter

	// Handle early errors
	if res.err != nil {
		sender.send(buildErrorFrame(res.options.RequestID, 400, res.err.Error()))
		return
	}

	// Handle SSE connections
	if res.sseClient != nil {
		dispatchSSEAsyncV2(res, sender)
		return
	}

	// Handle WebSocket connections
	if res.wsClient != nil {
		dispatchWebSocketAsyncV2(res, sender)
		return
	}

	if res.cancel != nil {
		defer res.cancel()
	}

	// Perform the HTTP request
	resp, err := res.client.Do(res.req)
	if err != nil {
		parsedError := parseError(err)
		sender.send(buildErrorFrame(res.options.RequestID, parsedError.StatusCode, parsedError.ErrorMsg+"-> \n"+err.Error()))
		return
	}
	defer resp.Body.Close()

	// Get final URL after redirects
	finalUrl := res.options.Options.URL
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		finalUrl = resp.Request.URL.String()
	}

	// Send response frame
	if !sender.send(buildResponseFrame(res.options.RequestID, resp.StatusCode, finalUrl, resp.Header)) {
		return
	}

	// Stream body with flow control
	const bufferSize = 8192
	chunkBuffer := make([]byte, bufferSize)
	ctx := res.ctx
	if ctx == nil {
		ctx = res.req.Context()
	}

	for {
		if ctx.Err() != nil {
			debugLogger.Printf("Request %s context canceled during processing", res.options.RequestID)
			return
		}

		n, readErr := resp.Body.Read(chunkBuffer)

		// Send any data we got before handling errors
		if n > 0 {
			if limiter != nil {
				if err := limiter.Acquire(int64(n), ctx); err != nil {
					return
				}
			}
			if !sender.send(buildDataFrame(res.options.RequestID, chunkBuffer[:n])) {
				return
			}
		}

		// Handle read result
		if readErr == io.EOF {
			sender.send(buildEndFrame(res.options.RequestID))
			return
		}
		if readErr != nil {
			parsedError := parseError(readErr)
			sender.send(buildErrorFrame(res.options.RequestID, parsedError.StatusCode, parsedError.ErrorMsg))
			return
		}
	}
}

// dispatchSSEAsyncV2 handles SSE connections with flow control.
func dispatchSSEAsyncV2(res fullRequest, sender *frameSender) {
	// For now, delegate to existing SSE handler with wrapper
	// TODO: Implement full SSE v2 support
	legacySender := &legacyFrameSenderWrapper{sender: sender}
	dispatchSSEAsync(res, legacySender.chanWrite)
}

// dispatchWebSocketAsyncV2 handles WebSocket connections with flow control.
func dispatchWebSocketAsyncV2(res fullRequest, sender *frameSender) {
	// For now, delegate to existing WebSocket handler with wrapper
	// TODO: Implement full WebSocket v2 support
	legacySender := &legacyFrameSenderWrapper{sender: sender}
	dispatchWebSocketAsync(res, legacySender.chanWrite)
}

// legacyFrameSenderWrapper wraps frameSender to provide legacy safeChannelWriter interface.
// TODO: This is incomplete - chanWrite is nil. SSE/WebSocket v2 needs proper implementation.
type legacyFrameSenderWrapper struct {
	sender    *frameSender
	chanWrite *safeChannelWriter
}
