package clerkoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	callbackPath     = "/callback"
	defaultTimeout   = 5 * time.Minute
	maxTokenResponse = 1 << 20
)

type Config struct {
	IssuerURL         string
	ClientID          string
	AllowCustomIssuer bool
	OpenBrowser       func(string) error
	Output            io.Writer
	HTTPClient        *http.Client
	Timeout           time.Duration
}

type Client struct {
	issuer      *url.URL
	clientID    string
	openBrowser func(string) error
	output      io.Writer
	httpClient  *http.Client
	timeout     time.Duration
}

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("browser authentication is not configured: missing Clerk OAuth client ID")
	}
	issuer, err := url.Parse(strings.TrimSpace(cfg.IssuerURL))
	if err != nil || issuer.Scheme == "" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || (issuer.Path != "" && issuer.Path != "/") {
		return nil, errors.New("invalid Clerk OAuth issuer")
	}
	if issuer.Scheme != "https" {
		ip := net.ParseIP(issuer.Hostname())
		if !cfg.AllowCustomIssuer || issuer.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("clerk OAuth issuer must use HTTPS")
		}
	}
	issuer.Path = ""
	openBrowser := cfg.OpenBrowser
	if openBrowser == nil {
		openBrowser = systemOpenBrowser
	}
	output := cfg.Output
	if output == nil {
		output = io.Discard
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OAuth redirect refused") }}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{issuer: issuer, clientID: strings.TrimSpace(cfg.ClientID), openBrowser: openBrowser, output: output, httpClient: httpClient, timeout: timeout}, nil
}

type callbackResult struct {
	code   string
	denied bool
}

func (c *Client) Run(ctx context.Context) (string, error) {
	state, err := randomValue()
	if err != nil {
		return "", errors.New("starting browser authentication")
	}
	verifier, err := randomValue()
	if err != nil {
		return "", errors.New("starting browser authentication")
	}
	challengeHash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", errors.New("starting local browser callback")
	}
	defer func() { _ = listener.Close() }()
	redirectURI := "http://" + listener.Addr().String() + callbackPath
	resultCh := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: callbackHandler(state, resultCh)}
	serveDone := make(chan struct{})
	go func() { _ = server.Serve(listener); close(serveDone) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-serveDone
	}()

	authorizeURL := *c.issuer
	authorizeURL.Path = "/oauth/authorize"
	query := authorizeURL.Query()
	query.Set("client_id", c.clientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", "profile")
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = query.Encode()
	if err := c.openBrowser(authorizeURL.String()); err != nil {
		_, _ = fmt.Fprintf(c.output, "Could not open a browser. Open this URL to continue:\n%s\n", authorizeURL.String())
	}

	flowCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case result := <-resultCh:
		if result.denied {
			return "", errors.New("browser sign-in was denied")
		}
		return c.exchangeCode(flowCtx, result.code, verifier, redirectURI)
	case <-flowCtx.Done():
		if errors.Is(flowCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("browser sign-in timed out")
		}
		return "", errors.New("browser sign-in canceled")
	}
}

func callbackHandler(expectedState string, resultCh chan<- callbackResult) http.Handler {
	var accepted atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		state := r.URL.Query().Get("state")
		if len(state) != len(expectedState) || subtle.ConstantTimeCompare([]byte(state), []byte(expectedState)) != 1 {
			http.Error(w, "Invalid sign-in callback.", http.StatusBadRequest)
			return
		}
		result := callbackResult{code: r.URL.Query().Get("code"), denied: r.URL.Query().Get("error") != ""}
		if result.code == "" && !result.denied {
			http.Error(w, "Invalid sign-in callback.", http.StatusBadRequest)
			return
		}
		if !accepted.CompareAndSwap(false, true) {
			http.Error(w, "Sign-in callback already received.", http.StatusConflict)
			return
		}
		select {
		case resultCh <- result:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "TTS Buddy CLI sign-in received. You can close this window.\n")
		default:
			http.Error(w, "Sign-in callback already received.", http.StatusConflict)
		}
	})
}

func (c *Client) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (string, error) {
	tokenURL := *c.issuer
	tokenURL.Path = "/oauth/token"
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("exchanging browser authorization")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.New("exchanging browser authorization")
	}
	defer func() { _ = resp.Body.Close() }()
	limited := io.LimitReader(resp.Body, maxTokenResponse+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil || len(body) > maxTokenResponse || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New("browser authorization exchange failed")
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &tokenResponse) != nil || tokenResponse.AccessToken == "" {
		return "", errors.New("browser authorization exchange returned an invalid response")
	}
	return tokenResponse.AccessToken, nil
}

func randomValue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func systemOpenBrowser(rawURL string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{rawURL}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		command, args = "xdg-open", []string{rawURL}
	}
	return exec.Command(command, args...).Start()
}
