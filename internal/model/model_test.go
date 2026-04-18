package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPreflightOpenAIOK(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if err := Preflight(context.Background(), "openai", "sk-test", srv.URL); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
}

func TestPreflightAnthropicOK(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if err := Preflight(context.Background(), "anthropic", "key-123", srv.URL); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if gotKey != "key-123" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotVersion == "" {
		t.Error("anthropic-version header missing")
	}
}

func TestPreflightUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	err := Preflight(context.Background(), "openai", "bad", srv.URL)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want to mention 401", err)
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("error = %v, want to include response body", err)
	}
}

func TestPreflightUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 127.0.0.1:1 is reserved and should refuse TCP immediately on most hosts.
	err := Preflight(ctx, "openai", "x", "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestPreflightUnknownProvider(t *testing.T) {
	err := Preflight(context.Background(), "nope", "x", "")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error = %v, want 'unknown provider'", err)
	}
}
