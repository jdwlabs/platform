package truenas

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsPath is the versioned JSON-RPC endpoint. It is deliberately not the REST
// prefix: TrueNAS removes REST in 26, and every REST call platformctl made
// would also keep the NAS's deprecation alert alive, which is the only live
// evidence that the CSI drivers still depend on the removed transport.
const wsPath = "/api/current"

const (
	dialTimeout = 20 * time.Second
	callTimeout = 60 * time.Second
)

// Caller issues one JSON-RPC 2.0 method call and decodes its result. The whole
// TrueNAS dependency is expressed through this one method, so the day upstream
// changes transports again the swap is this file and nothing else.
type Caller interface {
	Call(ctx context.Context, method string, params []any, out any) error
	Close() error
}

// TLSOptions controls certificate verification for the middleware connection.
//
// The API key is a bearer credential, and TrueNAS revokes user-linked keys that
// authenticate over plaintext, so the connection is always TLS — there is no
// ws:// option here on purpose. A stock NAS presents the self-signed
// `truenas_default` certificate (CN=localhost), which cannot verify against an
// IP address, so one of these two escapes is required until a real certificate
// is installed.
type TLSOptions struct {
	CAFile     string
	SkipVerify bool
}

// WSClient speaks JSON-RPC 2.0 over a single WebSocket connection.
//
// Calls are serialised rather than multiplexed. The middleware interleaves
// unsolicited notifications (collection_update and friends) with responses, so
// the read loop skips anything that is not the response to the call in flight;
// concurrency would buy nothing here because every caller in this package
// issues one query at a time.
type WSClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	next int64
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *rpcError) Error() string {
	if len(e.Data) == 0 {
		return fmt.Sprintf("truenas rpc error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("truenas rpc error %d: %s: %s", e.Code, e.Message, string(e.Data))
}

// ErrAPIKeyRejected is returned when the middleware accepts the connection and
// refuses the credential. It is a sentinel because the remedy is never "try
// again": an authentication attempt is a mutation, and repeating one against
// the shared democratic-csi key is what invalidated it in ADR-0025.
var ErrAPIKeyRejected = errors.New("TrueNAS rejected the API key")

// Dial opens the middleware connection and authenticates with the API key.
// The key is never stored on the returned client and never appears in an error
// string: an authentication failure reports the rejection, not the credential.
//
// One attempt, no retry, for the reason above.
func Dial(ctx context.Context, host string, apiKey string, tlsOpts TLSOptions) (*WSClient, error) {
	tlsCfg, err := buildTLSConfig(host, tlsOpts)
	if err != nil {
		return nil, err
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: dialTimeout,
		TLSClientConfig:  tlsCfg,
	}
	endpoint := (&url.URL{Scheme: "wss", Host: host, Path: wsPath}).String()

	conn, resp, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to TrueNAS middleware at %s: %w", endpoint, describeDialError(err, tlsOpts))
	}

	c := &WSClient{conn: conn}
	var ok bool
	if err := c.Call(ctx, "auth.login_with_api_key", []any{apiKey}, &ok); err != nil {
		_ = c.Close()
		if isCredentialRejection(err) {
			return nil, fmt.Errorf("%w: %w", ErrAPIKeyRejected, err)
		}
		return nil, fmt.Errorf("authenticate to TrueNAS: %w", err)
	}
	if !ok {
		_ = c.Close()
		return nil, ErrAPIKeyRejected
	}
	return c, nil
}

// Call sends one request and returns when its response arrives. out may be nil
// to discard the result.
func (c *WSClient) Call(ctx context.Context, method string, params []any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if params == nil {
		params = []any{}
	}
	c.next++
	id := c.next

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(callTimeout)
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set write deadline for %s: %w", method, err)
	}
	if err := c.conn.WriteJSON(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return fmt.Errorf("send %s: %w", method, err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			return fmt.Errorf("set read deadline for %s: %w", method, err)
		}
		var resp rpcResponse
		if err := c.conn.ReadJSON(&resp); err != nil {
			return fmt.Errorf("read response to %s: %w", method, err)
		}
		// A message with no id is a notification, and one with a different id
		// belongs to a call that already timed out. Neither answers this call.
		if resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("%s: %w", method, resp.Error)
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
		return nil
	}
}

// Standard JSON-RPC codes for a request the server rejected before dispatching
// it. The middleware returns these for a renamed or removed method, which is
// exactly what a future NAS release could do to auth.login_with_api_key.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

// isCredentialRejection reports whether an error from the login call means the
// key itself was refused.
//
// The middleware answers a bad key with an error as readily as with false, so a
// refusal from the method carries the same "do not repeat" meaning as a false.
// The codes above do not: they say the request never reached a point where a
// credential was evaluated, so nothing was spent and re-running is fine. The
// sentinel's whole value is that distinction — a "do not re-run, you are
// eroding the credential" warning printed for a renamed method is noise that
// teaches an operator to ignore the real one.
func isCredentialRejection(err error) bool {
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) {
		return false
	}
	switch rpcErr.Code {
	case rpcParseError, rpcInvalidRequest, rpcMethodNotFound, rpcInvalidParams:
		return false
	}
	return true
}

func (c *WSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Close()
}

func buildTLSConfig(host string, opts TLSOptions) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostnameOf(host)}
	if opts.SkipVerify {
		// #nosec G402 -- opt-in only, and still strictly safer than the
		// plaintext transport the CSI drivers are stuck on: the key stays
		// encrypted on the wire either way.
		cfg.InsecureSkipVerify = true
		return cfg, nil
	}
	if opts.CAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(opts.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle %s: %w", opts.CAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA bundle %s contains no PEM certificate", opts.CAFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

func hostnameOf(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// describeDialError names the remedy for the one failure a stock NAS always
// produces, because "x509: certificate signed by unknown authority" reads as a
// misconfiguration rather than as the shipped default it is.
func describeDialError(err error, opts TLSOptions) error {
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	if !errors.As(err, &unknownAuthority) && !errors.As(err, &hostnameErr) {
		return err
	}
	if opts.CAFile != "" {
		return fmt.Errorf("%w — the certificate does not chain to %s", err, opts.CAFile)
	}
	return fmt.Errorf("%w — a stock TrueNAS presents a self-signed certificate for CN=localhost; "+
		"pass --truenas-ca-file with the NAS certificate or --truenas-insecure-skip-tls-verify", err)
}
