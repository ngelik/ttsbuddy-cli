package clerkoauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRunCompletesAuthorizationCodePKCEFlow(t *testing.T) {
	var tokenForm url.Values
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.Method != http.MethodPost {
			t.Fatalf("token request = %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		tokenForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "oauth-access-fixture", "refresh_token": "must-not-be-returned"})
	}))
	defer issuer.Close()

	var opened *url.URL
	client, err := New(Config{
		IssuerURL: issuer.URL, ClientID: "client-fixture", AllowCustomIssuer: true,
		OpenBrowser: func(raw string) error {
			opened, _ = url.Parse(raw)
			go func() {
				callback := opened.Query().Get("redirect_uri") + "?code=authorization-code&state=" + url.QueryEscape(opened.Query().Get("state"))
				resp, requestErr := http.Get(callback)
				if requestErr == nil {
					_ = resp.Body.Close()
				}
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.Run(context.Background())
	if err != nil || token != "oauth-access-fixture" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if opened == nil || opened.Path != "/oauth/authorize" {
		t.Fatalf("authorization URL=%v", opened)
	}
	query := opened.Query()
	if query.Get("client_id") != "client-fixture" || query.Get("response_type") != "code" || query.Get("scope") != "profile" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization query=%v", query)
	}
	if len(query.Get("state")) < 43 || len(query.Get("code_challenge")) != 43 {
		t.Fatalf("weak state or challenge: %v", query)
	}
	if got := tokenForm.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type=%q", got)
	}
	if tokenForm.Get("client_id") != "client-fixture" || tokenForm.Get("code") != "authorization-code" {
		t.Fatalf("token form=%v", tokenForm)
	}
	verifier := tokenForm.Get("code_verifier")
	if len(verifier) < 43 || verifier == query.Get("code_challenge") {
		t.Fatalf("invalid verifier")
	}
	if tokenForm.Get("redirect_uri") != query.Get("redirect_uri") || !strings.HasPrefix(tokenForm.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(tokenForm.Get("redirect_uri"), "/callback") {
		t.Fatalf("redirect_uri=%q", tokenForm.Get("redirect_uri"))
	}
}

func TestRunRejectsWrongPathAndStateButAcceptsLaterValidCallback(t *testing.T) {
	issuer := tokenIssuer(t, "access-fixture")
	defer issuer.Close()
	client, err := New(Config{IssuerURL: issuer.URL, ClientID: "client", AllowCustomIssuer: true, OpenBrowser: func(raw string) error {
		authURL, _ := url.Parse(raw)
		redirect := authURL.Query().Get("redirect_uri")
		state := authURL.Query().Get("state")
		go func() {
			for _, callback := range []string{
				strings.Replace(redirect, "/callback", "/wrong", 1) + "?code=bad&state=" + state,
				redirect + "?code=bad&state=wrong",
				redirect + "?code=good&state=" + state,
			} {
				resp, requestErr := http.Get(callback)
				if requestErr == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}
		}()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if token, runErr := client.Run(context.Background()); runErr != nil || token != "access-fixture" {
		t.Fatalf("token=%q err=%v", token, runErr)
	}
}

func TestRunRejectsDuplicateValidCallback(t *testing.T) {
	releaseToken := make(chan struct{})
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-releaseToken
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "access-fixture"})
	}))
	defer issuer.Close()
	duplicateStatus := make(chan int, 1)
	client, err := New(Config{IssuerURL: issuer.URL, ClientID: "client", AllowCustomIssuer: true, OpenBrowser: func(raw string) error {
		authURL, _ := url.Parse(raw)
		callback := authURL.Query().Get("redirect_uri") + "?code=good&state=" + authURL.Query().Get("state")
		go func() {
			first, requestErr := http.Get(callback)
			if requestErr == nil {
				_ = first.Body.Close()
			}
			duplicate, requestErr := http.Get(callback)
			if requestErr != nil {
				duplicateStatus <- 0
			} else {
				duplicateStatus <- duplicate.StatusCode
				_ = duplicate.Body.Close()
			}
			close(releaseToken)
		}()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if token, runErr := client.Run(context.Background()); runErr != nil || token != "access-fixture" {
		t.Fatalf("token=%q err=%v", token, runErr)
	}
	if status := <-duplicateStatus; status != http.StatusConflict {
		t.Fatalf("duplicate status=%d", status)
	}
}

func TestRunReportsMatchingOAuthDenialWithoutLeakingDescription(t *testing.T) {
	secret := "provider-secret-description"
	client, err := New(Config{IssuerURL: "https://clerk.ttsbuddy.com", ClientID: "client", OpenBrowser: func(raw string) error {
		authURL, _ := url.Parse(raw)
		go func() {
			callback := authURL.Query().Get("redirect_uri") + "?error=access_denied&error_description=" + secret + "&state=" + authURL.Query().Get("state")
			resp, requestErr := http.Get(callback)
			if requestErr == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := client.Run(context.Background())
	if runErr == nil || strings.Contains(runErr.Error(), secret) || !strings.Contains(runErr.Error(), "denied") {
		t.Fatalf("err=%v", runErr)
	}
}

func TestRunPrintsManualURLWhenBrowserCannotOpen(t *testing.T) {
	var output strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	client, err := New(Config{IssuerURL: "https://clerk.ttsbuddy.com", ClientID: "client", Output: &output, OpenBrowser: func(string) error { cancel(); return context.Canceled }})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.Run(ctx)
	if !strings.Contains(output.String(), "https://clerk.ttsbuddy.com/oauth/authorize?") || !strings.Contains(output.String(), "Open this URL") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestRunHonorsTimeoutAndCancellation(t *testing.T) {
	for name, setup := range map[string]func() (context.Context, time.Duration){
		"timeout": func() (context.Context, time.Duration) { return context.Background(), time.Millisecond },
		"cancellation": func() (context.Context, time.Duration) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, time.Minute
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, timeout := setup()
			client, err := New(Config{IssuerURL: "https://clerk.ttsbuddy.com", ClientID: "client", Timeout: timeout, OpenBrowser: func(string) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if _, runErr := client.Run(ctx); runErr == nil {
				t.Fatal("flow unexpectedly succeeded")
			}
		})
	}
}

func TestNewRejectsMissingClientAndUnsafeIssuer(t *testing.T) {
	for _, cfg := range []Config{
		{IssuerURL: "https://clerk.ttsbuddy.com"},
		{IssuerURL: "http://example.com", ClientID: "client", AllowCustomIssuer: true},
		{IssuerURL: "http://127.0.0.1:1234", ClientID: "client"},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
}

func TestTokenExchangeErrorsDoNotLeakOAuthMaterial(t *testing.T) {
	secretResponse := "provider-body-private-fixture"
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, secretResponse)
	}))
	defer issuer.Close()
	client, err := New(Config{IssuerURL: issuer.URL, ClientID: "client", AllowCustomIssuer: true, OpenBrowser: func(raw string) error {
		authURL, _ := url.Parse(raw)
		go func() {
			callback := authURL.Query().Get("redirect_uri") + "?code=authorization-code-private&state=" + authURL.Query().Get("state")
			resp, requestErr := http.Get(callback)
			if requestErr == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := client.Run(context.Background())
	if runErr == nil || strings.Contains(runErr.Error(), secretResponse) || strings.Contains(runErr.Error(), "authorization-code-private") {
		t.Fatalf("err=%v", runErr)
	}
}

func tokenIssuer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	}))
}
