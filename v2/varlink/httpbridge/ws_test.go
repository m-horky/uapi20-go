package httpbridge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckWebSocketOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		host   string
		wantOK bool
	}{
		{"no origin header", "", "bridge.example", true},
		{"same origin", "https://bridge.example", "bridge.example", true},
		{"same origin with port", "http://bridge.example:8080", "bridge.example:8080", true},
		{"cross origin", "https://evil.example", "bridge.example", false},
		{"cross origin port mismatch", "http://bridge.example:9999", "bridge.example", false},
		{"scheme-only difference is same host", "http://bridge.example", "bridge.example", true},
		{"invalid origin", "://", "bridge.example", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/ws/services/x", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			err := checkWebSocketOrigin(nil, req)
			if tt.wantOK && err != nil {
				t.Fatalf("checkWebSocketOrigin() error = %v, want nil", err)
			}
			if !tt.wantOK && err == nil {
				t.Fatal("checkWebSocketOrigin() unexpectedly succeeded")
			}
		})
	}
}
