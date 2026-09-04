package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestCLIAuthBrowserExchangeSendsExplicitMethodJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer oauth-proof-fixture" || r.Header.Get("Content-Type") != "application/json" || string(body) != `{"method":"browser"}` {
			t.Fatalf("method=%s auth=%q content-type=%q body=%q", r.Method, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body)
		}
		_ = json.NewEncoder(w).Encode(CLIAuthResponse{Success: true})
	}))
	defer srv.Close()
	client, err := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "oauth-proof-fixture", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	if response, _, exchangeErr := client.ExchangeBrowser(context.Background()); exchangeErr != nil || response == nil || !response.Success {
		t.Fatalf("response=%#v err=%v", response, exchangeErr)
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

func TestCLIAuthClientFollowsSameOriginRedirectWithoutDroppingBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cli-auth" {
			http.Redirect(w, r, "/v1/cli-auth-final", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path != "/v1/cli-auth-final" || r.Header.Get("Authorization") != "Bearer bearer_fixture" {
			t.Fatalf("redirect request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(CLIAuthResponse{Success: true})
	}))
	defer srv.Close()
	client, err := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "bearer_fixture", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	response, _, err := client.Status(context.Background())
	if err != nil || response == nil || !response.Success {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestCLIAuthClientAllowsSupabaseFunctionPathOnlyOnLoopback(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:54321/functions/v1/cli-auth",
		"http://localhost:54321/functions/v1/cli-auth",
		"http://[::1]:54321/functions/v1/cli-auth",
	} {
		if _, err := NewCLIAuthClient(raw, "secret", "test", true); err != nil {
			t.Fatalf("rejected loopback local function URL %q: %v", raw, err)
		}
	}
	if _, err := NewCLIAuthClient("https://example.com/functions/v1/cli-auth", "secret", "test", true); err == nil {
		t.Fatal("accepted non-loopback Supabase function path")
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

func TestCLIAuthClientPreservesDistinctHTTPStatusClasses(t *testing.T) {
	for _, status := range []int{401, 403, 409, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			client, err := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "bearer_fixture", "test", true)
			if err != nil {
				t.Fatal(err)
			}
			_, got, requestErr := client.Status(context.Background())
			var httpErr *CLIAuthHTTPError
			if got != status || !errors.As(requestErr, &httpErr) || httpErr.StatusCode != status {
				t.Fatalf("status=%d error=%#v", got, requestErr)
			}
		})
	}
}

func TestCLIAuthClientHonorsCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	client, err := NewCLIAuthClient(srv.URL+"/v1/cli-auth", "bearer_fixture", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := client.Status(ctx); err == nil {
		t.Fatal("canceled request succeeded")
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
