package webpage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestFetchArticleExtractsReadableHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "ttsbuddy-cli/test") {
			t.Fatalf("User-Agent = %q, want ttsbuddy-cli/test prefix", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head><title>Ignored Browser Title</title></head>
  <body>
    <nav>Navigation should disappear</nav>
    <main>
      <article>
        <h1>Readable Article Title</h1>
        <p>This is the first useful paragraph for narration.</p>
        <p>This second paragraph has enough article content to be extracted.</p>
      </article>
    </main>
  </body>
</html>`))
	}))
	defer srv.Close()

	article, err := fetchArticle(context.Background(), srv.URL, "test", fetchOptions{allowInitialPrivateNetwork: true})
	if err != nil {
		t.Fatalf("FetchArticle error: %v", err)
	}

	if article.URL != srv.URL {
		t.Fatalf("URL = %q, want %q", article.URL, srv.URL)
	}
	if article.Title != "Readable Article Title" {
		t.Fatalf("Title = %q, want readable title", article.Title)
	}
	if !strings.Contains(article.Text, "first useful paragraph") {
		t.Fatalf("extracted text missing article body: %q", article.Text)
	}
	if strings.Contains(article.Text, "Navigation should disappear") {
		t.Fatalf("extracted text should not include navigation: %q", article.Text)
	}
}

func TestFetchArticleRejectsUnsupportedURLScheme(t *testing.T) {
	_, err := FetchArticle(context.Background(), "file:///tmp/article.html", "test")
	if err == nil {
		t.Fatal("expected error for file URL")
	}
	if !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("error = %q, want scheme guidance", err.Error())
	}
}

func TestFetchArticleRejectsDirectPrivateNetworkURL(t *testing.T) {
	_, err := FetchArticle(context.Background(), "http://127.0.0.1/private", "test")
	if err == nil {
		t.Fatal("expected error for private network URL")
	}
	if !strings.Contains(err.Error(), "private network") {
		t.Fatalf("error = %q, want private network", err.Error())
	}
}

func TestFetchArticleRejectsRedirectToPrivateNetworkURL(t *testing.T) {
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Internal</h1><p>private text should not be fetched.</p></body></html>`))
	}))
	defer private.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := fetchArticle(context.Background(), redirector.URL, "test", fetchOptions{allowInitialPrivateNetwork: true})
	if err == nil {
		t.Fatal("expected error for redirect to private network URL")
	}
	if !strings.Contains(err.Error(), "private network") {
		t.Fatalf("error = %q, want private network", err.Error())
	}
}

func TestFetchArticleTestOptionAllowsInitialPrivateNetworkURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Local Test</h1><p>Readable local text for test-only webpage fetching.</p></body></html>`))
	}))
	defer srv.Close()

	article, err := fetchArticle(context.Background(), srv.URL, "test", fetchOptions{allowInitialPrivateNetwork: true})
	if err != nil {
		t.Fatalf("fetchArticle error: %v", err)
	}
	if article.Title != "Local Test" {
		t.Fatalf("Title = %q, want Local Test", article.Title)
	}
}

func TestNewWebClientDisablesProxyForProductionSSRFProtection(t *testing.T) {
	client := newWebClient(false)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("production client transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("production webpage fetches must disable proxies so private-network dial validation applies to the final destination")
	}
	if transport.DialTLSContext != nil {
		t.Fatal("production webpage fetches must leave DialTLSContext nil so HTTPS uses the guarded DialContext")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "100.64.0.1:443"); err == nil {
		t.Fatal("production DialContext accepted special-use private-network address")
	} else if !strings.Contains(err.Error(), "private network") {
		t.Fatalf("DialContext error = %q, want private network", err.Error())
	}

	testClient := newWebClient(true)
	if testClient.Transport != nil {
		t.Fatalf("test-only private-dial client transport = %T, want nil default transport", testClient.Transport)
	}
}

func TestIsPrivateWebAddr(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		private bool
	}{
		{name: "nil", private: true},
		{name: "public IPv4 Cloudflare", ip: "1.1.1.1", private: false},
		{name: "public IPv4 Google", ip: "8.8.8.8", private: false},
		{name: "public IPv6 Cloudflare", ip: "2606:4700:4700::1111", private: false},
		{name: "IPv4 current network", ip: "0.0.0.1", private: true},
		{name: "IPv4 RFC1918 10", ip: "10.0.0.1", private: true},
		{name: "IPv4 CGNAT", ip: "100.64.0.1", private: true},
		{name: "IPv4 loopback", ip: "127.0.0.1", private: true},
		{name: "IPv4 link local", ip: "169.254.1.1", private: true},
		{name: "IPv4 RFC1918 172", ip: "172.16.0.1", private: true},
		{name: "IPv4 IETF protocol assignments", ip: "192.0.0.1", private: true},
		{name: "IPv4 TEST-NET-1", ip: "192.0.2.1", private: true},
		{name: "IPv4 AS112", ip: "192.31.196.1", private: true},
		{name: "IPv4 AMT", ip: "192.52.193.1", private: true},
		{name: "IPv4 6to4 relay anycast", ip: "192.88.99.1", private: true},
		{name: "IPv4 RFC1918 192", ip: "192.168.0.1", private: true},
		{name: "IPv4 direct delegation AS112", ip: "192.175.48.1", private: true},
		{name: "IPv4 benchmark", ip: "198.18.0.1", private: true},
		{name: "IPv4 TEST-NET-2", ip: "198.51.100.1", private: true},
		{name: "IPv4 TEST-NET-3", ip: "203.0.113.1", private: true},
		{name: "IPv4 multicast", ip: "224.0.0.1", private: true},
		{name: "IPv4 future use", ip: "240.0.0.1", private: true},
		{name: "IPv4 limited broadcast", ip: "255.255.255.255", private: true},
		{name: "IPv6 unspecified", ip: "::", private: true},
		{name: "IPv6 loopback", ip: "::1", private: true},
		{name: "IPv6 IPv4-mapped", ip: "::ffff:192.0.2.1", private: true},
		{name: "IPv6 IPv4-mapped public", ip: "::ffff:8.8.8.8", private: true},
		{name: "IPv6 well-known translation", ip: "64:ff9b::1", private: true},
		{name: "IPv6 local-use translation", ip: "64:ff9b:1::1", private: true},
		{name: "IPv6 discard-only", ip: "100::1", private: true},
		{name: "IPv6 dummy prefix", ip: "100:0:0:1::1", private: true},
		{name: "IPv6 IETF protocol assignments", ip: "2001::1", private: true},
		{name: "IPv6 benchmarking", ip: "2001:2::1", private: true},
		{name: "IPv6 AS112", ip: "2001:4:112::1", private: true},
		{name: "IPv6 ORCHIDv2", ip: "2001:20::1", private: true},
		{name: "IPv6 DETs", ip: "2001:30::1", private: true},
		{name: "IPv6 documentation", ip: "2001:db8::1", private: true},
		{name: "IPv6 6to4", ip: "2002::1", private: true},
		{name: "IPv6 direct delegation AS112", ip: "2620:4f:8000::1", private: true},
		{name: "IPv6 documentation 3fff", ip: "3fff::1", private: true},
		{name: "IPv6 SRv6 SIDs", ip: "5f00::1", private: true},
		{name: "IPv6 ULA", ip: "fc00::1", private: true},
		{name: "IPv6 link local", ip: "fe80::1", private: true},
		{name: "IPv6 multicast", ip: "ff02::1", private: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ip == "" {
				if got := isPrivateWebAddr(netip.Addr{}); got != tt.private {
					t.Fatalf("isPrivateWebAddr(netip.Addr{}) = %v, want %v", got, tt.private)
				}
				return
			}
			addr, err := netip.ParseAddr(tt.ip)
			if err != nil {
				t.Fatalf("failed to parse test IP %q: %v", tt.ip, err)
			}
			if got := isPrivateWebAddr(addr); got != tt.private {
				t.Fatalf("isPrivateWebAddr(%q) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}
