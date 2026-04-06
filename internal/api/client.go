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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

	return parseResponse(resp)
}

// DownloadAudio downloads an audio file atomically.
// Writes to destPath.part first, then renames to destPath on success.
// Cleans up the .part file on error.
func (c *Client) DownloadAudio(ctx context.Context, audioURL, destPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return fmt.Errorf("creating download request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("downloading audio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	partPath := destPath + ".part"

	f, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}

	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()

	if copyErr != nil {
		os.Remove(partPath)
		return fmt.Errorf("writing audio data: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(partPath)
		return fmt.Errorf("closing temp file: %w", closeErr)
	}

	if err := os.Rename(partPath, destPath); err != nil {
		os.Remove(partPath)
		return fmt.Errorf("finalizing download: %w", err)
	}

	return nil
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voice API returned status %d", resp.StatusCode)
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
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

func parseResponse(resp *http.Response) (*TTSResponse, int, error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", err)
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

func statusToErrorCode(status int) string {
	switch {
	case status == 401:
		return ErrInvalidKey
	case status == 403:
		return ErrInactiveSubscription
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
