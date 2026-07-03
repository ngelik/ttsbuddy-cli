# Medium Security Findings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the four medium-severity findings from the Codex Security scan without weakening existing CLI behavior, then prove the fixes with focused unit tests, full Go test gates, and live production TTS smoke queries.

**Architecture:** Keep security policy close to the affected boundary: webpage destination policy in `internal/webpage`, authenticated API redirect policy in `internal/api`, audio URL success validation at the `cmd/speak.go` completion boundary, and immutable action pinning in workflow configuration plus a small workflow-policy check. Tests should be hermetic for security controls and use the live API only after local tests pass.

**Tech Stack:** Go 1.25 module, Cobra CLI, `net/http`, `httptest`, shell scripts, GitHub Actions, `jq` for live smoke verification.

---

## Findings Covered

1. `CAND-AUDIOURL-JSON-002`: JSON and no-download modes report unsafe audio URLs as successful output.
2. `CAND-API-REDIRECT-001`: API redirects can replay bearer tokens and request bodies to another host.
3. `CAND-WEB-SSRF-001`: Webpage conversion can submit private-network HTML to the TTS API.
4. `CAND-RELEASE-MUTABLE-ACTIONS-001`: Release workflow uses mutable actions while holding release tokens.

The temporary API key from the thread may be used for live smoke testing, but do not write it into this file, source code, tests, scripts, shell history, or logs. Load it interactively into `TTSBUDDY_TEMP_API_KEY` during the verification task.

## File Structure

- Modify `cmd/speak.go`: centralize completed-response audio URL validation before every success output branch.
- Modify `cmd/speak_test.go`: add subprocess regression tests for unsafe `audio_url` in `--json` and `--no-download`.
- Modify `internal/api/client.go`: add same-origin redirect policy for authenticated API requests.
- Modify `internal/api/client_test.go`: add redirect regression tests for `Speak` and `GetStatus`.
- Modify `internal/webpage/webpage.go`: add public-destination enforcement for initial webpage URLs, redirects, and resolved dial targets.
- Modify `internal/webpage/webpage_test.go`: test private URL and private redirect rejection plus safe extraction through a test-only allowance.
- Modify `cmd/web.go` and `cmd/testutil_test.go`: add a package-level fetch hook for subprocess tests so command tests do not need private `httptest` webpage fetches after production blocking.
- Modify `cmd/web_test.go`: update current web command tests to use the fetch hook; add one subprocess-level rejection test.
- Modify `.github/workflows/release.yml`: pin release workflow actions to immutable commit SHAs resolved during implementation.
- Modify `.github/workflows/ci.yml`: pin CI action references too, because the same mutable pattern exists there.
- Create `scripts/check-github-actions-pinned.sh`: fail if any workflow `uses:` target is not pinned to a 40-character SHA.
- Modify `Makefile`: add `check-actions-pinned` and include it in the final verification checklist.

---

### Task 1: Validate API audio URLs before JSON or no-download success output

**Files:**
- Modify: `cmd/speak.go`
- Modify: `cmd/speak_test.go`

- [ ] **Step 1: Add failing tests for unsafe URL success output**

Append these tests near `TestSpeakMissingAudioURL` in `cmd/speak_test.go`:

```go
func TestSpeakJSONRejectsUnsafeAudioURL(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "unsafe-json",
			"audio_url": "http://evil.example/audio.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--json")
	assertExitCode(t, r, 1)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "CLI_ERROR", "stdout")
	assertContains(t, r.Stdout, "refusing to download over insecure HTTP", "stdout")
	assertNotContains(t, r.Stdout, "evil.example/audio.mp3", "stdout")
}

func TestSpeakNoDownloadRejectsUnsafeAudioURL(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "unsafe-no-download",
			"audio_url": "http://evil.example/audio.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--no-download")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "refusing to download over insecure HTTP", "stderr")
	assertNotContains(t, r.Stderr, "evil.example/audio.mp3", "stderr")
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./cmd -run 'TestSpeak(JSON|NoDownload)RejectsUnsafeAudioURL' -count=1
```

Expected: both new tests fail because `handleCompleted` currently accepts any non-empty `audio_url` in those success modes.

- [ ] **Step 3: Add one validation helper in `cmd/speak.go`**

Insert this helper above `handleCompleted`:

```go
func validateCompletedAudioURL(resp *api.TTSResponse, resolved *config.ResolvedConfig) error {
	if resp.AudioURL == "" {
		return &exitError{code: 1, msg: "completed but no audio URL in response"}
	}
	apiHost := apiHostFromURL(resolved.APIURL)
	if err := api.ValidateDownloadURL(resp.AudioURL, apiHost); err != nil {
		return &exitError{code: 1, msg: err.Error()}
	}
	return nil
}
```

Keep the helper in `cmd/speak.go`; do not export new API package surface unless another package needs it.

- [ ] **Step 4: Call the helper before every success branch**

Replace the initial `resp.AudioURL == ""` block in `handleCompleted` with:

```go
	if err := validateCompletedAudioURL(resp, resolved); err != nil {
		return err
	}
```

This must run before `flagJSON`, `speakNoDownload`, `speakOutput == "-"`, and normal file download handling.

- [ ] **Step 5: Run the focused tests and verify they pass**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./cmd -run 'TestSpeak(JSON|NoDownload)RejectsUnsafeAudioURL|TestSpeakJSON|TestSpeakNoDownload|TestSpeakMissingAudioURL' -count=1
```

Expected: all listed tests pass. `TestSpeakJSON` and `TestSpeakNoDownload` must still succeed with allowed localhost or Amazon S3-style mock URLs.

- [ ] **Step 6: Commit this task**

```bash
git add cmd/speak.go cmd/speak_test.go
git commit -m "fix: validate completed audio urls before success output"
```

---

### Task 2: Prevent authenticated API redirects from crossing origin boundaries

**Files:**
- Modify: `internal/api/client.go`
- Modify: `internal/api/client_test.go`

- [ ] **Step 1: Add failing redirect tests**

Append these tests after `TestGetStatus404` in `internal/api/client_test.go`:

```go
func TestSpeakRejectsCrossOriginRedirectWithoutLeakingAuth(t *testing.T) {
	var redirectedAuth string
	var redirectedBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		redirectedBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := NewClient(redirector.URL, "test_key", "test")
	_, _, err := client.Speak(context.Background(), SpeakRequest{Text: "secret text"}, "idem")
	if err == nil {
		t.Fatal("expected cross-origin redirect rejection")
	}
	if redirectedAuth != "" {
		t.Fatalf("redirect target received Authorization: %q", redirectedAuth)
	}
	if redirectedBody != "" {
		t.Fatalf("redirect target received body: %q", redirectedBody)
	}
	if !strings.Contains(err.Error(), "cross-origin API redirect") {
		t.Fatalf("error = %v, want cross-origin API redirect", err)
	}
}

func TestGetStatusRejectsCrossOriginRedirectWithoutLeakingAuth(t *testing.T) {
	var redirectedAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := NewClient(redirector.URL, "test_key", "test")
	_, _, err := client.GetStatus(context.Background(), "job-123")
	if err == nil {
		t.Fatal("expected cross-origin redirect rejection")
	}
	if redirectedAuth != "" {
		t.Fatalf("redirect target received Authorization: %q", redirectedAuth)
	}
}

func TestSpeakAllowsSameOriginRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
		case "/final":
			if r.Header.Get("Authorization") != "Bearer test_key" {
				t.Fatalf("missing Authorization on same-origin redirect")
			}
			_ = json.NewEncoder(w).Encode(TTSResponse{
				Success:  true,
				Status:   "completed",
				JobID:    "same-origin",
				AudioURL: "https://example.com/audio.mp3",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/start", "test_key", "test")
	resp, _, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem")
	if err != nil {
		t.Fatalf("same-origin redirect should pass: %v", err)
	}
	if resp.JobID != "same-origin" {
		t.Fatalf("job_id = %q, want same-origin", resp.JobID)
	}
}
```

Add `io` to the import list in `internal/api/client_test.go`.

- [ ] **Step 2: Run the focused tests and verify the cross-origin tests fail**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./internal/api -run 'Test(Speak|GetStatus).*Redirect' -count=1
```

Expected: cross-origin redirect tests fail because the default client follows redirects.

- [ ] **Step 3: Add same-origin redirect policy helpers**

In `internal/api/client.go`, add helpers near `NewClient`:

```go
func newAPIHTTPClient(apiURL string) *http.Client {
	origin := apiOrigin(apiURL)
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many API redirects")
			}
			if origin == nil || !sameOrigin(req.URL, origin) {
				return fmt.Errorf("refusing cross-origin API redirect to %s", req.URL.Redacted())
			}
			return nil
		},
	}
}

func apiOrigin(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

func sameOrigin(candidate, origin *url.URL) bool {
	if candidate == nil || origin == nil {
		return false
	}
	return strings.EqualFold(candidate.Scheme, origin.Scheme) &&
		strings.EqualFold(candidate.Host, origin.Host)
}
```

This compares scheme plus host plus port through `URL.Host`, not only hostname.

- [ ] **Step 4: Use the policy in `NewClient`**

Replace the current `httpClient: &http.Client{},` line with:

```go
		httpClient: newAPIHTTPClient(apiURL),
```

- [ ] **Step 5: Run redirect tests and existing API tests**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./internal/api -run 'Test(Speak|GetStatus).*Redirect|TestSpeak200Completed|TestGetStatus200Completed|TestRetry' -count=1
```

Expected: all listed tests pass. The same-origin redirect remains allowed; cross-origin targets receive no auth or body.

- [ ] **Step 6: Commit this task**

```bash
git add internal/api/client.go internal/api/client_test.go
git commit -m "fix: constrain authenticated api redirects"
```

---

### Task 3: Block private-network webpage destinations before TTS submission

**Files:**
- Modify: `internal/webpage/webpage.go`
- Modify: `internal/webpage/webpage_test.go`
- Modify: `cmd/web.go`
- Modify: `cmd/testutil_test.go`
- Modify: `cmd/web_test.go`

- [ ] **Step 1: Add failing package-level webpage destination tests**

Append these tests to `internal/webpage/webpage_test.go`:

```go
func TestFetchArticleRejectsDirectPrivateNetworkURL(t *testing.T) {
	_, err := FetchArticle(context.Background(), "http://127.0.0.1/private", "test")
	if err == nil {
		t.Fatal("expected private-network URL rejection")
	}
	if !strings.Contains(err.Error(), "private network") {
		t.Fatalf("error = %q, want private network", err.Error())
	}
}

func TestFetchArticleRejectsRedirectToPrivateNetworkURL(t *testing.T) {
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Internal</h1><p>secret internal text should not leave.</p></body></html>`))
	}))
	defer private.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := fetchArticle(context.Background(), redirector.URL, "test", fetchOptions{
		allowInitialPrivateNetwork: true,
	})
	if err == nil {
		t.Fatal("expected redirect to private network to be rejected")
	}
	if !strings.Contains(err.Error(), "private network") {
		t.Fatalf("error = %q, want private network", err.Error())
	}
}

func TestFetchArticleTestOptionAllowsInitialPrivateNetworkURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><article><h1>Local Test</h1><p>Readable local test article text.</p></article></body></html>`))
	}))
	defer srv.Close()

	article, err := fetchArticle(context.Background(), srv.URL, "test", fetchOptions{
		allowInitialPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("fetchArticle error: %v", err)
	}
	if article.Title != "Local Test" {
		t.Fatalf("title = %q, want Local Test", article.Title)
	}
}
```

The unexported `fetchArticle` and `fetchOptions` do not exist yet, so this should fail to compile before implementation.

Also update the existing `TestFetchArticleExtractsReadableHTML` call from:

```go
	article, err := FetchArticle(context.Background(), srv.URL, "test")
```

to:

```go
	article, err := fetchArticle(context.Background(), srv.URL, "test", fetchOptions{
		allowInitialPrivateNetwork: true,
	})
```

That existing test is still valuable for extraction behavior, but it should not require production code to allow loopback webpage fetches.

- [ ] **Step 2: Add failing command-level test**

Append this test to `cmd/web_test.go`:

```go
func TestWebCommandRejectsPrivateNetworkURLBeforeSubmit(t *testing.T) {
	home := t.TempDir()
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Internal</h1><p>private text should not be submitted.</p></body></html>`))
	}))
	defer private.Close()

	submitted := false
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted = true
		w.WriteHeader(http.StatusInternalServerError)
	}))

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "web", private.URL, "--no-download")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "private network", "stderr")
	if submitted {
		t.Fatal("web command submitted private-network content to API")
	}
}
```

- [ ] **Step 3: Run focused tests and verify failure**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./internal/webpage ./cmd -run 'TestFetchArticle.*Private|TestWebCommandRejectsPrivateNetworkURLBeforeSubmit' -count=1
```

Expected: package tests fail to compile until the options helper exists, and command test fails until production blocks private destinations.

- [ ] **Step 4: Implement destination classification in `internal/webpage/webpage.go`**

Add imports:

```go
	"net"
```

Add options, public-client construction, and destination helpers below the constants:

```go
type fetchOptions struct {
	allowInitialPrivateNetwork bool
}

func newWebClient(allowPrivateDial bool) *http.Client {
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects while fetching webpage")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirected to unsupported URL scheme %q; use http or https", req.URL.Scheme)
			}
			if err := validateWebDestination(req.Context(), req.URL, false); err != nil {
				return err
			}
			return nil
		},
	}
	if allowPrivateDial {
		return client
	}

	dialer := &net.Dialer{Timeout: fetchTimeout}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("checking webpage destination %q: %w", address, err)
		}
		ips, err := publicWebIPs(ctx, host, false)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	client.Transport = transport
	return client
}

func fetchArticle(ctx context.Context, rawURL, version string, opts fetchOptions) (*Article, error) {
	parsed, err := validateURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateWebDestination(ctx, parsed, opts.allowInitialPrivateNetwork); err != nil {
		return nil, err
	}

	return fetchArticleWithClient(ctx, newWebClient(opts.allowInitialPrivateNetwork), parsed, version)
}

func validateWebDestination(ctx context.Context, u *url.URL, allowPrivate bool) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("invalid webpage URL: %s", u.String())
	}
	if allowPrivate {
		return nil
	}
	_, err := publicWebIPs(ctx, host, false)
	return err
}

func publicWebIPs(ctx context.Context, host string, allowPrivate bool) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivate && isPrivateWebIP(ip) {
			return nil, fmt.Errorf("refusing to fetch private network webpage host %q", host)
		}
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving webpage host %q: %w", host, err)
	}
	result := make([]net.IP, 0, len(ips))
	for _, addr := range ips {
		if !allowPrivate && isPrivateWebIP(addr.IP) {
			return nil, fmt.Errorf("refusing to fetch private network webpage host %q", host)
		}
		result = append(result, addr.IP)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("webpage host %q resolved no addresses", host)
	}
	return result, nil
}

func isPrivateWebIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}
```

Refactor the current `FetchArticle` body so it becomes:

```go
func FetchArticle(ctx context.Context, rawURL, version string) (*Article, error) {
	return fetchArticle(ctx, rawURL, version, fetchOptions{})
}
```

Move the existing request/read/parse body into:

```go
func fetchArticleWithClient(ctx context.Context, client *http.Client, parsed *url.URL, version string) (*Article, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating webpage request: %w", err)
	}
	req.Header.Set("User-Agent", "ttsbuddy-cli/"+version)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching webpage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Paste the current status check, content-type check, bounded read,
	// readability extraction, fallback extraction, length checks, title
	// fallback, and final Article return block from the existing FetchArticle
	// implementation here without changing that extraction logic.
}
```

When moving code, do not change the extraction behavior except for destination validation.

- [ ] **Step 5: Keep existing command tests hermetic without private production fetches**

In `cmd/web.go`, add this package variable near the command definition:

```go
var fetchArticleForWeb = webpage.FetchArticle
```

Change the production call:

```go
	article, err := webpage.FetchArticle(ctx, rawURL, Version)
```

to:

```go
	article, err := fetchArticleForWeb(ctx, rawURL, Version)
```

In `cmd/testutil_test.go`, inside `TestHelperProcess` after the args setup and before `Execute()`, add:

```go
	if os.Getenv("TTSBUDDY_TEST_FAKE_WEB_ARTICLE") == "1" {
		fetchArticleForWeb = func(ctx context.Context, rawURL, version string) (*webpage.Article, error) {
			return &webpage.Article{
				URL:   rawURL,
				Title: "Docs Page",
				Text:  "Readable article text for the web command test.",
			}, nil
		}
	}
```

Add imports to `cmd/testutil_test.go`:

```go
	"context"

	"github.com/ngelik/ttsbuddy-cli/internal/webpage"
```

For every existing `cmd/web_test.go` test that expects a successful local `httptest` webpage fetch, append this env var to `envForTest(...)`:

```go
append(envForTest(home, apiSrv, "ttsb_test_key"), "TTSBUDDY_TEST_FAKE_WEB_ARTICLE=1")
```

Do not add that env var to `TestWebCommandRejectsPrivateNetworkURLBeforeSubmit`; that test must exercise the production rejection path.

- [ ] **Step 6: Run webpage and web command focused tests**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./internal/webpage ./cmd -run 'TestFetchArticle|TestWebCommand' -count=1
```

Expected: all webpage package tests and web command tests pass. The private-network rejection test must fail before any API submission.

- [ ] **Step 7: Commit this task**

```bash
git add internal/webpage/webpage.go internal/webpage/webpage_test.go cmd/web.go cmd/testutil_test.go cmd/web_test.go
git commit -m "fix: block private webpage destinations"
```

---

### Task 4: Pin GitHub Actions release and CI workflow dependencies

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/ci.yml`
- Create: `scripts/check-github-actions-pinned.sh`
- Modify: `Makefile`

- [ ] **Step 1: Create a workflow pinning check**

Create `scripts/check-github-actions-pinned.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

bad=0
while IFS= read -r line; do
  ref="${line##*@}"
  if [[ ! "$ref" =~ ^[0-9a-f]{40}$ ]]; then
    echo "Unpinned GitHub Action reference: $line" >&2
    bad=1
  fi
done < <(grep -RhoE 'uses:[[:space:]]+[^[:space:]]+@[^[:space:]]+' .github/workflows | sed -E 's/^[[:space:]]*uses:[[:space:]]+//')

if [[ "$bad" -ne 0 ]]; then
  echo "Pin workflow actions to full 40-character commit SHAs." >&2
  exit 1
fi
```

Make it executable:

```bash
chmod +x scripts/check-github-actions-pinned.sh
```

- [ ] **Step 2: Add Makefile target**

Add `check-actions-pinned` to `.PHONY`:

```make
.PHONY: build test test-live test-acceptance lint tools release-snapshot clean check-actions-pinned
```

Add this target:

```make
check-actions-pinned:
	./scripts/check-github-actions-pinned.sh
```

- [ ] **Step 3: Verify the check fails before pinning**

Run:

```bash
make check-actions-pinned
```

Expected: failure listing `actions/checkout@v6`, `actions/setup-go@v6`, `goreleaser/goreleaser-action@v7`, and `golangci/golangci-lint-action@v9`.

- [ ] **Step 4: Resolve current immutable SHAs**

Run these commands at implementation time:

```bash
CHECKOUT_SHA="$(git ls-remote https://github.com/actions/checkout.git refs/tags/v6 | awk '{print $1}')"
SETUP_GO_SHA="$(git ls-remote https://github.com/actions/setup-go.git refs/tags/v6 | awk '{print $1}')"
GORELEASER_SHA="$(git ls-remote https://github.com/goreleaser/goreleaser-action.git refs/tags/v7 | awk '{print $1}')"
GOLANGCI_SHA="$(git ls-remote https://github.com/golangci/golangci-lint-action.git refs/tags/v9 | awk '{print $1}')"
printf 'checkout=%s\nsetup-go=%s\ngoreleaser=%s\ngolangci=%s\n' "$CHECKOUT_SHA" "$SETUP_GO_SHA" "$GORELEASER_SHA" "$GOLANGCI_SHA"
```

Expected: each value is exactly 40 lowercase hex characters. If any command returns empty output, stop and diagnose network or tag availability before editing workflows.

- [ ] **Step 5: Apply SHA pinning mechanically**

Run:

```bash
python3 - <<'PY'
import os
from pathlib import Path

replacements = {
    "actions/checkout@v6": "actions/checkout@" + os.environ["CHECKOUT_SHA"],
    "actions/setup-go@v6": "actions/setup-go@" + os.environ["SETUP_GO_SHA"],
    "goreleaser/goreleaser-action@v7": "goreleaser/goreleaser-action@" + os.environ["GORELEASER_SHA"],
    "golangci/golangci-lint-action@v9": "golangci/golangci-lint-action@" + os.environ["GOLANGCI_SHA"],
}

for path in [Path(".github/workflows/release.yml"), Path(".github/workflows/ci.yml")]:
    text = path.read_text()
    for old, new in replacements.items():
        text = text.replace(old, new)
    path.write_text(text)
PY
```

If the shell variables are not exported, run:

```bash
export CHECKOUT_SHA SETUP_GO_SHA GORELEASER_SHA GOLANGCI_SHA
```

Then rerun the Python command.

- [ ] **Step 6: Verify pinning and workflow syntax**

Run:

```bash
make check-actions-pinned
git diff -- .github/workflows/release.yml .github/workflows/ci.yml scripts/check-github-actions-pinned.sh Makefile
```

Expected: the pinning check passes, and every workflow `uses:` reference ends in a 40-character SHA. Keep the existing `with:` blocks and token environment unchanged.

- [ ] **Step 7: Commit this task**

```bash
git add .github/workflows/release.yml .github/workflows/ci.yml scripts/check-github-actions-pinned.sh Makefile
git commit -m "fix: pin github actions workflows"
```

---

### Task 5: Full local verification and live production smoke tests

**Files:**
- No source modifications unless a test reveals a bug.

- [ ] **Step 1: Run formatting**

Run:

```bash
gofmt -w cmd/speak.go cmd/speak_test.go cmd/web.go cmd/testutil_test.go cmd/web_test.go internal/api/client.go internal/api/client_test.go internal/webpage/webpage.go internal/webpage/webpage_test.go
```

Expected: no output. Review `git diff` afterward.

- [ ] **Step 2: Run focused security regression tests**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test ./cmd ./internal/api ./internal/webpage \
  -run 'TestSpeak(JSON|NoDownload)RejectsUnsafeAudioURL|Test(Speak|GetStatus).*Redirect|TestFetchArticle.*Private|TestWebCommandRejectsPrivateNetworkURLBeforeSubmit' \
  -count=1
```

Expected: pass.

- [ ] **Step 3: Run full unit suite with race detector**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  go test -race -count=1 ./...
```

Expected: pass.

- [ ] **Step 4: Run lint and workflow pin check**

Run:

```bash
make check-actions-pinned
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  make lint
```

Expected: `make check-actions-pinned` passes. `make lint` passes; if `golangci-lint` is missing, run `make tools` first with the same temp Go cache environment.

- [ ] **Step 5: Build the CLI**

Run:

```bash
env GOCACHE=/private/tmp/ttsbuddy-go-build-cache \
  GOMODCACHE=/private/tmp/ttsbuddy-go-mod-cache \
  make build
```

Expected: `bin/ttsbuddy` exists and is executable.

- [ ] **Step 6: Load the temporary live API key without writing it to disk**

Run:

```bash
read -r -s TTSBUDDY_TEMP_API_KEY
export TTSBUDDY_API_KEY="$TTSBUDDY_TEMP_API_KEY"
export TB_HOME="$(mktemp -d /tmp/ttsbuddy-live-home.XXXXXX)"
export TB_OUT="$(mktemp -d /tmp/ttsbuddy-live-out.XXXXXX)"
```

Paste the temporary key from the thread when prompted by `read -s`. Do not echo the variable.

- [ ] **Step 7: Run live production JSON/no-download smoke**

Run:

```bash
HOME="$TB_HOME" TTSBUDDY_API_KEY="$TTSBUDDY_API_KEY" \
  bin/ttsbuddy speak "Codex security JSON smoke $(date -u +%Y%m%dT%H%M%SZ)" \
  --voice af_heart --json --no-download --timeout 10m >"$TB_OUT/speak-json.json"

jq -e '.status == "completed" or .status == "processing"' "$TB_OUT/speak-json.json"
jq -e '.audio_url or .status_url' "$TB_OUT/speak-json.json"
```

Expected: command exits 0, JSON is valid, and the response has either `audio_url` or `status_url`.

- [ ] **Step 8: Poll live production status if needed**

Run:

```bash
JOB_ID="$(jq -r '.job_id // empty' "$TB_OUT/speak-json.json")"
test -n "$JOB_ID"

HOME="$TB_HOME" TTSBUDDY_API_KEY="$TTSBUDDY_API_KEY" \
  bin/ttsbuddy status "$JOB_ID" --watch --timeout 10m --json >"$TB_OUT/status.json"

jq -e '.status == "completed"' "$TB_OUT/status.json"
```

Expected: status reaches `completed`.

- [ ] **Step 9: Run live production download smoke**

Wait at least 65 seconds after the prior POST to avoid rate-limit noise:

```bash
sleep 65
HOME="$TB_HOME" TTSBUDDY_API_KEY="$TTSBUDDY_API_KEY" \
  bin/ttsbuddy speak "Codex security download smoke $(date -u +%Y%m%dT%H%M%SZ)" \
  --voice af_heart --timeout 10m -o "$TB_OUT/download-smoke.mp3"

test -s "$TB_OUT/download-smoke.mp3"
file "$TB_OUT/download-smoke.mp3"
```

Expected: command exits 0, output file exists and is non-empty, and `file` reports audio or binary data.

- [ ] **Step 10: Run live production webpage smoke**

Wait at least 65 seconds after the prior POST:

```bash
sleep 65
HOME="$TB_HOME" TTSBUDDY_API_KEY="$TTSBUDDY_API_KEY" \
  bin/ttsbuddy web "https://www.ttsbuddy.com/docs/" \
  --voice af_heart --json --no-download --timeout 10m >"$TB_OUT/web-json.json"

jq -e '.status == "completed" or .status == "processing"' "$TB_OUT/web-json.json"
jq -e '.audio_url or .status_url' "$TB_OUT/web-json.json"
```

Expected: public webpage conversion still works after private-network blocking.

- [ ] **Step 11: Clean live-test secrets from shell**

Run:

```bash
unset TTSBUDDY_API_KEY TTSBUDDY_TEMP_API_KEY
rm -rf "$TB_HOME" "$TB_OUT"
unset TB_HOME TB_OUT JOB_ID
```

Expected: no secret remains in the shell environment or temp directories created by the smoke test.

- [ ] **Step 12: Final review**

Run:

```bash
git status --short
git diff --check
git log --oneline -4
```

Expected: only intended commits are present, whitespace check passes, and no temporary files or API keys are tracked.

---

## Self-Review Checklist

- Spec coverage: all four medium findings have a dedicated task with tests and implementation steps.
- Secret handling: the temporary API key is never written into repo files or command history; it is loaded with `read -s`.
- Regression strength: each code finding has a failing unit/subprocess test before implementation and a focused pass command after implementation.
- Live coverage: final verification includes real production `speak`, `status`, download, and `web` queries through `bin/ttsbuddy`.
- Release safety: workflow pinning is both remediated and guarded by `make check-actions-pinned`.
