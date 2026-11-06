// Package httpbridge implements the UAPI.20 Varlink HTTP transport: it
// encapsulates varlink services behind an HTTP endpoint, making them
// available to environments where the AF_UNIX transport is not available.
//
// Two modes are served: a simple HTTP mode in which each HTTP request maps
// to one varlink method call, and a WebSocket mode that tunnels the varlink
// wire protocol verbatim. See the URL schema in the specification for the
// endpoints served under the bridge's base URL.
//
// Since neither HTTP nor WebSocket connections can carry file descriptors,
// methods relying on file descriptor passing are not available through the
// bridge; the bridge does not accept file descriptors from the services
// behind it, so they are dropped rather than forwarded. Parameters that
// reference file descriptor indices are still forwarded verbatim: detecting
// such references requires interface-specific knowledge, so interfaces that
// pass file descriptors should document how their methods fail when used
// through the bridge.
package httpbridge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/m-horky/uapi20-go/v2/varlink"
	"github.com/m-horky/uapi20-go/v2/varlink/idl"
)

// Config configures a Bridge.
type Config struct {
	// Services maps service names to the varlink addresses (unix: or
	// tcp:) they are served on.
	Services map[string]string

	// Client is the client policy used for connections to the services.
	// It must not enable AcceptFileDescriptors: the bridge cannot forward
	// file descriptors, so it must not accept them.
	Client varlink.ClientConfig

	// TLS, if set, is used to serve HTTPS and WSS. For mutual TLS, set
	// ClientCAs and ClientAuth = tls.RequireAndVerifyClientCert on it;
	// clients then have to present a certificate signed by the CA.
	TLS *tls.Config

	// Authorize, if set, is called before a request is processed.
	// Returning an error fails the request with 401 Unauthorized.
	Authorize func(r *http.Request) error

	// MaxBodySize bounds the size of simple-mode request bodies. It
	// defaults to varlink.MaxFrameSizeDefault.
	MaxBodySize int64
}

// Bridge serves a set of varlink services over HTTP.
type Bridge struct {
	cfg      Config
	services map[string]string
	names    []string
}

// New validates cfg and returns a Bridge for it.
func New(cfg Config) (*Bridge, error) {
	if len(cfg.Services) == 0 {
		return nil, errors.New("httpbridge: no services configured")
	}
	if cfg.Client.AcceptFileDescriptors {
		return nil, errors.New("httpbridge: the bridge must not accept file descriptors")
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = varlink.MaxFrameSizeDefault
	}
	b := &Bridge{
		cfg:      cfg,
		services: make(map[string]string, len(cfg.Services)),
	}
	for name, address := range cfg.Services {
		if _, err := varlink.ParseAddress(address); err != nil {
			return nil, fmt.Errorf("httpbridge: service %q: %w", name, err)
		}
		b.services[name] = address
		b.names = append(b.names, name)
	}
	sort.Strings(b.names)
	return b, nil
}

// ServiceNames returns the sorted names of the served services.
func (b *Bridge) ServiceNames() []string {
	return append([]string(nil), b.names...)
}

// Handler returns the HTTP handler implementing the bridge. It serves the
// URL schema relative to the root path; mount it under a prefix with
// http.StripPrefix to change the base URL.
func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", b.handleHealth)
	mux.HandleFunc("GET /services", b.handleServices)
	mux.HandleFunc("GET /services/{service}", b.handleServiceInfo)
	mux.HandleFunc("GET /services/{service}/{interface}", b.handleServiceMethods)
	mux.HandleFunc("POST /call/{method}", b.handleCall)
	mux.HandleFunc("POST /call/{service}/{method}", b.handleCall)
	mux.HandleFunc("GET /ws/services/{service}", b.handleWebSocket)
	return mux
}

// ListenAndServe listens on addr (host:port) and serves the bridge until
// the context is canceled or the listener fails. It serves HTTPS and WSS
// when Config.TLS is set, plain HTTP and WS otherwise.
func (b *Bridge) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	server := &http.Server{
		Handler:           b.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		TLSConfig:         b.cfg.TLS,
	}
	if b.cfg.TLS != nil {
		err = server.ServeTLS(ln, "", "")
	} else {
		err = server.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return ctx.Err()
	}
	return err
}

// authorize runs the configured authorization hook.
func (b *Bridge) authorize(w http.ResponseWriter, r *http.Request) bool {
	if b.cfg.Authorize == nil {
		return true
	}
	if err := b.cfg.Authorize(r); err != nil {
		writeMessage(w, http.StatusUnauthorized, err.Error())
		return false
	}
	return true
}

func (b *Bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *Bridge) handleServices(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Services []string `json:"services"`
	}{Services: b.names})
}

func (b *Bridge) handleServiceInfo(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	address, ok := b.lookupService(w, r.PathValue("service"))
	if !ok {
		return
	}
	client, err := varlink.DialWithConfig(r.Context(), address, b.cfg.Client)
	if err != nil {
		writeMessage(w, http.StatusBadGateway, err.Error())
		return
	}
	defer client.Close()
	info, interfaces, err := client.GetInfo(r.Context())
	if err != nil {
		writeCallError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Vendor     string   `json:"vendor"`
		Product    string   `json:"product"`
		Version    string   `json:"version"`
		URL        string   `json:"url"`
		Interfaces []string `json:"interfaces"`
	}{
		Vendor:     info.Vendor,
		Product:    info.Product,
		Version:    info.Version,
		URL:        info.URL,
		Interfaces: interfaces,
	})
}

func (b *Bridge) handleServiceMethods(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	address, ok := b.lookupService(w, r.PathValue("service"))
	if !ok {
		return
	}
	ifaceName := r.PathValue("interface")
	if !strings.Contains(ifaceName, ".") {
		writeMessage(w, http.StatusBadRequest, "invalid interface name")
		return
	}
	client, err := varlink.DialWithConfig(r.Context(), address, b.cfg.Client)
	if err != nil {
		writeMessage(w, http.StatusBadGateway, err.Error())
		return
	}
	defer client.Close()
	description, err := client.GetInterfaceDescription(r.Context(), ifaceName)
	if err != nil {
		writeCallError(w, err)
		return
	}
	parsed, err := idl.Parse(description)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	var methods []string
	for _, member := range parsed.Members {
		if method, ok := member.(*idl.Method); ok {
			methods = append(methods, method.Name)
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Methods []string `json:"methods"`
	}{Methods: methods})
}

// handleCall implements the simple HTTP mode.
func (b *Bridge) handleCall(w http.ResponseWriter, r *http.Request) {
	if !b.authorize(w, r) {
		return
	}
	service := r.PathValue("service")
	method := r.PathValue("method")
	if !strings.Contains(method, ".") {
		writeMessage(w, http.StatusBadRequest, "method must be fully qualified")
		return
	}
	if service == "" {
		service = r.URL.Query().Get("service")
	}
	if service == "" {
		// Derive the service from the interface prefix of the method
		// name, by stripping the last component.
		i := strings.LastIndex(method, ".")
		service = method[:i]
	}
	address, ok := b.lookupService(w, service)
	if !ok {
		return
	}

	// The request body is the value of the parameters member of the call,
	// not the call object itself; an empty body is equivalent to {}.
	parameters, err := b.readParameters(r)
	if err != nil {
		writeMessage(w, http.StatusBadRequest, err.Error())
		return
	}

	client, err := varlink.DialWithConfig(r.Context(), address, b.cfg.Client)
	if err != nil {
		writeMessage(w, http.StatusBadGateway, err.Error())
		return
	}
	defer client.Close()

	if r.URL.Query().Get("more") == "true" {
		b.streamCall(w, r, client, method, parameters)
		return
	}

	var out json.RawMessage
	if err := client.Invoke(r.Context(), method, parameters, &out); err != nil {
		writeCallError(w, err)
		return
	}
	writeUnwrapped(w, http.StatusOK, out)
}

// streamCall streams the replies of a more call as a JSON text sequence.
func (b *Bridge) streamCall(w http.ResponseWriter, r *http.Request, client *varlink.ConnClient, method string, parameters json.RawMessage) {
	stream, err := client.Stream(r.Context(), method, parameters)
	if err != nil {
		writeCallError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json-seq")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	for {
		var out json.RawMessage
		err := stream.Recv(r.Context(), &out)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			// The status line has already been sent, so failures
			// terminate the sequence with an error record.
			var remote *varlink.RemoteError
			if errors.As(err, &remote) {
				writeSeqRecord(w, errorBody{
					Error:      remote.Name,
					Parameters: remoteParameters(remote),
				})
			} else {
				writeSeqRecord(w, struct {
					Error string `json:"error"`
				}{Error: err.Error()})
			}
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		writeSeqRecord(w, unnil(out))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// readParameters reads the request body as the parameters object of a call.
func (b *Bridge) readParameters(r *http.Request) (json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, b.cfg.MaxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if int64(len(body)) > b.cfg.MaxBodySize {
		return nil, errors.New("request body too large")
	}
	var check json.RawMessage
	if err := json.Unmarshal(body, &check); err != nil {
		return nil, errors.New("invalid JSON body")
	}
	trimmed := strings.TrimSpace(string(check))
	if trimmed == "null" || trimmed[0] != '{' {
		return nil, errors.New("request body must be a JSON object")
	}
	return check, nil
}

// lookupService resolves a service name, replying 404 for unknown names.
func (b *Bridge) lookupService(w http.ResponseWriter, name string) (string, bool) {
	address, ok := b.services[name]
	if !ok {
		writeMessage(w, http.StatusNotFound, "unknown service "+name)
		return "", false
	}
	return address, true
}

// writeCallError maps a failed call to the HTTP status codes of the
// specification.
func writeCallError(w http.ResponseWriter, err error) {
	var remote *varlink.RemoteError
	if !errors.As(err, &remote) {
		// Transport failure: the bridge could not connect to the
		// service, or the connection failed while the call was in
		// progress.
		writeMessage(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, errorStatus(remote.Name), errorBody{
		Error:      remote.Name,
		Parameters: remoteParameters(remote),
	})
}

func remoteParameters(remote *varlink.RemoteError) json.RawMessage {
	var params json.RawMessage
	if len(remote.Parameters) > 0 {
		if err := remote.Decode(&params); err == nil && len(params) > 0 {
			return params
		}
	}
	return json.RawMessage(`{}`)
}

// errorStatus maps a varlink error to the HTTP status code required by the
// specification.
func errorStatus(name string) int {
	switch name {
	case varlink.ErrorInvalidParameter, varlink.ErrorExpectedMore, varlink.ErrorExpectedUpgrade:
		return http.StatusBadRequest
	case varlink.ErrorInterfaceNotFound, varlink.ErrorMethodNotFound:
		return http.StatusNotFound
	case varlink.ErrorMethodNotImplemented:
		return http.StatusNotImplemented
	case varlink.ErrorPermissionDenied:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

type errorBody struct {
	Error      string          `json:"error"`
	Parameters json.RawMessage `json:"parameters"`
}

// writeUnwrapped writes the output parameters of a reply as the response
// body; an absent parameters member is returned as {}.
func writeUnwrapped(w http.ResponseWriter, status int, out json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(unnil(out))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Message string `json:"message"`
	}{Message: message})
}

// writeSeqRecord writes one record of a JSON text sequence: a record
// separator, the JSON text and a line feed.
func writeSeqRecord(w io.Writer, v any) {
	_, _ = w.Write([]byte{0x1e})
	_ = json.NewEncoder(w).Encode(v)
}

func unnil(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
