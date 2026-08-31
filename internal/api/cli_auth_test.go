package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIAuthClientMethodsHeadersAndResponse(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.URL.Path != "/v1/cli-auth" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer bearer_fixture" || r.Header.Get("User-Agent") != "ttsbuddy-cli/test" {
					t.Fatal("missing headers")
				}
				_ = json.NewEncoder(w).Encode(CLIAuthResponse{Success: true, Status: "revoked"})
			}))
			defer srv.Close()
			client, err := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "bearer_fixture", "test", true)
			if err != nil {
				t.Fatal(err)
			}
			var response *CLIAuthResponse
			switch method {
			case http.MethodPost:
				response, _, err = client.Exchange(context.Background())
			case http.MethodGet:
				response, _, err = client.Status(context.Background())
			default:
				response, _, err = client.Revoke(context.Background())
			}
			if err != nil || !response.Success {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		})
	}
}

func TestCLIAuthClientRejectsUnsafeURLsAndCrossOriginRedirects(t *testing.T) {
	for _, raw := range []string{"http://example.com/v1/cli-auth", "https://other.example/v1/cli-auth", "https://www.ttsbuddy.com/other"} {
		if _, err := NewCLIAuthClient(raw, "secret", "test", false); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	targetContacted := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetContacted = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, _ := NewCLIAuthClient(source.URL+"/v1/cli-auth", "secret", "test", true)
	_, _, err := client.Status(context.Background())
	if err == nil || targetContacted {
		t.Fatal("cross-origin redirect was followed")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("secret leaked")
	}
}

func TestCLIAuthClientErrorsAreTypedBoundedAndBodyFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "23")
		w.WriteHeader(429)
		_, _ = w.Write([]byte("raw-secret-body"))
	}))
	defer srv.Close()
	client, _ := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "bearer_fixture", "test", true)
	_, status, err := client.Status(context.Background())
	if status != 429 {
		t.Fatalf("status=%d", status)
	}
	var httpErr *CLIAuthHTTPError
	if !errors.As(err, &httpErr) || httpErr.RetryAfterSeconds != 23 {
		t.Fatalf("error=%#v", err)
	}
	if strings.Contains(err.Error(), "raw-secret") || strings.Contains(err.Error(), "bearer_fixture") {
		t.Fatal("secret leaked")
	}
}

func TestCLIAuthClientRejectsMalformedAndOversizedSuccessBodies(t *testing.T) {
	for name, body := range map[string]string{"malformed": "{", "oversized": strings.Repeat("x", maxCLIAuthBody+1)} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			client, _ := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "secret_fixture", "test", true)
			_, _, err := client.Status(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), body) || strings.Contains(err.Error(), "secret_fixture") {
				t.Fatal("response or bearer leaked")
			}
		})
	}
}
