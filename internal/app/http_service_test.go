package app

import (
	"net/http"
	"testing"
	"time"

	"github.com/dujiao-next/internal/config"
)

func TestNewHTTPServiceAppliesServerTimeouts(t *testing.T) {
	service := NewHTTPService("127.0.0.1:0", http.NewServeMux(), config.ServerConfig{
		ReadHeaderTimeoutSeconds: 7,
		ReadTimeoutSeconds:       31,
		WriteTimeoutSeconds:      61,
		IdleTimeoutSeconds:       121,
		MaxHeaderBytes:           2048,
	})

	if service.server.ReadHeaderTimeout != 7*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 7s", service.server.ReadHeaderTimeout)
	}
	if service.server.ReadTimeout != 31*time.Second {
		t.Fatalf("ReadTimeout = %s, want 31s", service.server.ReadTimeout)
	}
	if service.server.WriteTimeout != 61*time.Second {
		t.Fatalf("WriteTimeout = %s, want 61s", service.server.WriteTimeout)
	}
	if service.server.IdleTimeout != 121*time.Second {
		t.Fatalf("IdleTimeout = %s, want 121s", service.server.IdleTimeout)
	}
	if service.server.MaxHeaderBytes != 2048 {
		t.Fatalf("MaxHeaderBytes = %d, want 2048", service.server.MaxHeaderBytes)
	}
}

func TestNewHTTPServiceFallsBackToDefaultMaxHeaderBytes(t *testing.T) {
	service := NewHTTPService("127.0.0.1:0", http.NewServeMux(), config.ServerConfig{})

	if service.server.MaxHeaderBytes != http.DefaultMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", service.server.MaxHeaderBytes, http.DefaultMaxHeaderBytes)
	}
}
