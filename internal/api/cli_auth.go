package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

const maxCLIAuthBody = 1 << 20

type CLIAuthCredential struct {
	Token     string `json:"token,omitempty"`
	Type      string `json:"type"`
	Scope     string `json:"scope,omitempty"`
	Status    string `json:"status,omitempty"`
	Usable    bool   `json:"usable"`
	ExpiresAt string `json:"expires_at,omitempty"`
}
type CLIEntitlement struct {
	Status    string `json:"status"`
	APIAccess bool   `json:"api_access"`
}
type CLIAuthResponse struct {
	Success     bool               `json:"success"`
	Status      string             `json:"status,omitempty"`
	Replaced    bool               `json:"replaced,omitempty"`
	Credential  *CLIAuthCredential `json:"credential,omitempty"`
	Entitlement *CLIEntitlement    `json:"entitlement,omitempty"`
}

type CLIAuthHTTPError struct {
	StatusCode        int
	RetryAfterSeconds int
}

func (e *CLIAuthHTTPError) Error() string {
	return fmt.Sprintf("CLI authentication request failed with status %d", e.StatusCode)
}

type CLIAuthClient struct {
	httpClient           *http.Client
	url, bearer, version string
}

func NewCLIAuthClient(baseURL, bearerToken, version string, allowCustom bool) (*CLIAuthClient, error) {
	baseURL = strings.TrimSpace(baseURL)
	if err := config.CheckCredentialedAPIURL(baseURL, allowCustom); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid CLI auth URL")
	}
	if parsed.Path != "/v1/cli-auth" {
		return nil, errors.New("CLI auth URL must end with /v1/cli-auth")
	}
	origin := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many CLI auth redirects")
		}
		if !sameOrigin(req.URL, origin) {
			return errors.New("refusing cross-origin CLI auth redirect")
		}
		return nil
	}}
	return &CLIAuthClient{httpClient: client, url: parsed.String(), bearer: bearerToken, version: version}, nil
}

func (c *CLIAuthClient) Exchange(ctx context.Context) (*CLIAuthResponse, int, error) {
	return c.do(ctx, http.MethodPost)
}
func (c *CLIAuthClient) Status(ctx context.Context) (*CLIAuthResponse, int, error) {
	return c.do(ctx, http.MethodGet)
}
func (c *CLIAuthClient) Revoke(ctx context.Context) (*CLIAuthResponse, int, error) {
	return c.do(ctx, http.MethodDelete)
}

func (c *CLIAuthClient) do(ctx context.Context, method string) (*CLIAuthResponse, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.url, nil)
	if err != nil {
		return nil, 0, errors.New("creating CLI authentication request")
	}
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	req.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, errors.New("sending CLI authentication request")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if retry < 1 || retry > 300 {
			retry = 0
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCLIAuthBody+1))
		return nil, resp.StatusCode, &CLIAuthHTTPError{StatusCode: resp.StatusCode, RetryAfterSeconds: retry}
	}
	limited := io.LimitReader(resp.Body, maxCLIAuthBody+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > maxCLIAuthBody {
		return nil, resp.StatusCode, errors.New("invalid CLI authentication response")
	}
	var result CLIAuthResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, resp.StatusCode, errors.New("invalid CLI authentication response")
	}
	return &result, resp.StatusCode, nil
}
