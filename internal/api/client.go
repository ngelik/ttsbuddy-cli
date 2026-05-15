package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is the HTTP client for the agent-tts API.
type Client struct {
	httpClient *http.Client
	apiURL     string
	apiKey     string
	version    string
}

// NewClient creates a new API client.
func NewClient(apiURL, apiKey, version string) *Client {
	return &Client{
		httpClient: &http.Client{},
		apiURL:     strings.TrimRight(apiURL, "/"),
		apiKey:     apiKey,
		version:    version,
	}
}

// Speak submits a TTS request. Returns the parsed response, HTTP status code, and any error.
func (c *Client) Speak(ctx context.Context, req SpeakRequest, idempotencyKey string) (*TTSResponse, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)
	if idempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseResponse(resp)
}

// GetStatus checks job status. Returns the parsed response and any error.
func (c *Client) GetStatus(ctx context.Context, jobID string) (*TTSResponse, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	reqURL := c.apiURL + "?id=" + url.QueryEscape(jobID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseResponse(resp)
}

// maxAudioSize caps audio downloads to prevent disk/memory exhaustion.
const maxAudioSize = 500 * 1024 * 1024 // 500MB

// NewDownloadClient creates an HTTP client with redirect validation.
// Each redirect hop is validated against the same scheme + host policy.
func NewDownloadClient(apiHost string) *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return ValidateDownloadURL(req.URL.String(), apiHost)
		},
	}
}

// DownloadAudio downloads an audio file atomically.
// Validates URL scheme + host, uses unique temp file, caps size at 500MB.
func (c *Client) DownloadAudio(ctx context.Context, audioURL, destPath string) error {
	_, err := c.DownloadAudioWithSize(ctx, audioURL, destPath)
	return err
}

// DownloadAudioWithSize downloads an audio file atomically and returns bytes written.
func (c *Client) DownloadAudioWithSize(ctx context.Context, audioURL, destPath string) (int64, error) {
	apiHost := apiHostFromURL(c.apiURL)
	if err := ValidateDownloadURL(audioURL, apiHost); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating download request: %w", err)
	}

	dlClient := NewDownloadClient(apiHost)
	resp, err := dlClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("downloading audio: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	dir := filepath.Dir(destPath)
	f, err := os.CreateTemp(dir, filepath.Base(destPath)+".part.*")
	if err != nil {
		return 0, fmt.Errorf("creating temp file: %w", err)
	}
	tmp := f.Name()

	written, copyErr := CopyBounded(f, resp.Body, maxAudioSize)
	closeErr := f.Close()

	if copyErr != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("writing audio data: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("closing temp file: %w", closeErr)
	}

	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("finalizing download: %w", err)
	}

	return written, nil
}

// ValidateDownloadURL checks that a download URL uses an allowed scheme and host.
// Allowed: HTTPS to API host or *.amazonaws.com; HTTP only for localhost.
func ValidateDownloadURL(rawURL, apiHost string) error {
	u, err := url.Parse(rawURL)
	if err != nil || rawURL == "" {
		return fmt.Errorf("invalid download URL: %v", err)
	}
	host := u.Hostname()

	switch u.Scheme {
	case "https":
		if allowedDownloadHost(host, apiHost) {
			return nil
		}
		return fmt.Errorf("download host %q not in allowed list (API host, *.amazonaws.com, or localhost)", host)
	case "http":
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("refusing to download over insecure HTTP from %s", host)
	default:
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
}

// allowedDownloadHost checks if the host is trusted for audio downloads.
func allowedDownloadHost(host, apiHost string) bool {
	if host == apiHost {
		return true
	}
	if strings.HasSuffix(host, ".amazonaws.com") {
		return true
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return false
}

// apiHostFromURL extracts the hostname from a URL string.
func apiHostFromURL(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// CopyBounded copies up to maxBytes from src to dst. Returns an error if the
// source has more data than maxBytes (hard fail, not truncate).
func CopyBounded(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, maxBytes))
	if err != nil {
		return written, err
	}
	// Probe for more data beyond the limit
	probe := make([]byte, 1)
	if n, _ := src.Read(probe); n > 0 {
		return written, fmt.Errorf("download exceeds %dMB size limit", maxBytes/1024/1024)
	}
	return written, nil
}

// FetchVoices retrieves the voice catalog from the upstream TTS API.
// This is a public endpoint — no API key required.
func (c *Client) FetchVoices(ctx context.Context, ttsAPIBaseURL string) ([]Voice, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	voiceURL := strings.TrimRight(ttsAPIBaseURL, "/") + "/api/v1/voices"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, voiceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating voice request: %w", err)
	}
	httpReq.Header.Set("User-Agent", "ttsbuddy-cli/"+c.version)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetching voices: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voice API returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading voice response: %w", err)
	}
	if len(data) > maxResponseSize {
		return nil, fmt.Errorf("voice response too large (>%dMB)", maxResponseSize/1024/1024)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decoding voice response: %w", err)
	}

	return parseVoiceResponse(raw)
}

// ResolveStatusURL prepends the API base URL to a relative status_url.
func (c *Client) ResolveStatusURL(statusURL string) string {
	if strings.HasPrefix(statusURL, "http") {
		return statusURL
	}
	parsed, err := url.Parse(c.apiURL)
	if err != nil {
		return c.apiURL + statusURL
	}
	return fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, statusURL)
}

// RetryAfterHeader extracts the Retry-After header value in seconds. Returns 0 if absent.
func RetryAfterHeader(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0
	}
	var seconds int
	if _, err := fmt.Sscanf(val, "%d", &seconds); err != nil {
		return 0
	}
	return seconds
}

// maxResponseSize caps API response reads to prevent memory exhaustion.
const maxResponseSize = 10 * 1024 * 1024 // 10MB

func parseResponse(resp *http.Response) (*TTSResponse, int, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", err)
	}
	if len(data) > maxResponseSize {
		// Wrap as APIResponseError so retry logic treats it correctly based on
		// HTTP status (e.g., oversized 401 fails immediately, not retried).
		synthetic := TTSResponse{
			Success: false,
			Error: &APIError{
				Code:    statusToErrorCode(resp.StatusCode),
				Message: fmt.Sprintf("response too large (>%dMB, HTTP %d)", maxResponseSize/1024/1024, resp.StatusCode),
			},
		}
		return &synthetic, resp.StatusCode, &APIResponseError{
			StatusCode: resp.StatusCode,
			Response:   synthetic,
		}
	}

	var ttsResp TTSResponse
	if err := json.Unmarshal(data, &ttsResp); err != nil {
		// Non-JSON response (e.g., reverse proxy HTML error page).
		// For error status codes, synthesize an APIResponseError so retry
		// and user-guidance logic still works based on HTTP status.
		if resp.StatusCode >= 400 {
			body := string(data)
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			synthetic := TTSResponse{
				Success: false,
				Error: &APIError{
					Code:    statusToErrorCode(resp.StatusCode),
					Message: fmt.Sprintf("HTTP %d (non-JSON response): %s", resp.StatusCode, body),
				},
			}
			return &synthetic, resp.StatusCode, &APIResponseError{
				StatusCode: resp.StatusCode,
				Response:   synthetic,
			}
		}
		return nil, resp.StatusCode, fmt.Errorf("parsing response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode >= 400 {
		return &ttsResp, resp.StatusCode, &APIResponseError{
			StatusCode: resp.StatusCode,
			Response:   ttsResp,
		}
	}

	return &ttsResp, resp.StatusCode, nil
}

// ErrForbidden is used for synthetic (non-JSON) 403 responses where the
// server didn't provide a specific error code. The real API distinguishes
// INACTIVE_SUBSCRIPTION, NO_API_ACCESS, and USAGE_LIMIT_EXCEEDED in JSON.
const ErrForbidden = "FORBIDDEN"

func statusToErrorCode(status int) string {
	switch {
	case status == 401:
		return ErrInvalidKey
	case status == 403:
		return ErrForbidden
	case status == 404:
		return ErrNotFound
	case status == 429:
		return ErrRateLimited
	case status >= 500:
		return ErrInternalError
	default:
		return ErrInvalidRequest
	}
}
