package webpage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

const (
	maxHTMLBytes     = 6 * 1024 * 1024
	maxArticleRunes  = 500_000
	minReadableRunes = 40
	fetchTimeout     = 20 * time.Second
	maxRedirects     = 5
)

var privateWebPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:4:112::/48"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:30::/28"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// Article is readable webpage content ready to submit for TTS.
type Article struct {
	URL   string
	Title string
	Text  string
}

type fetchOptions struct {
	allowInitialPrivateNetwork bool
}

// FetchArticle fetches a public HTTP(S) page and extracts readable article text.
func FetchArticle(ctx context.Context, rawURL, version string) (*Article, error) {
	return fetchArticle(ctx, rawURL, version, fetchOptions{})
}

func fetchArticle(ctx context.Context, rawURL, version string, opts fetchOptions) (*Article, error) {
	parsed, err := validateURL(rawURL)
	if err != nil {
		return nil, err
	}
	if !opts.allowInitialPrivateNetwork {
		if err := validateWebDestination(ctx, parsed, false); err != nil {
			return nil, err
		}
	}

	return fetchArticleWithClient(ctx, newWebClient(opts.allowInitialPrivateNetwork), parsed, version)
}

func fetchArticleWithClient(ctx context.Context, client *http.Client, parsed *url.URL, version string) (*Article, error) {
	if client == nil {
		client = newWebClient(false)
	}

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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("webpage returned status %d", resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "html") {
		return nil, fmt.Errorf("webpage content type %q is not HTML", resp.Header.Get("Content-Type"))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading webpage: %w", err)
	}
	if len(data) > maxHTMLBytes {
		return nil, fmt.Errorf("webpage HTML exceeds %dMB limit", maxHTMLBytes/1024/1024)
	}

	finalURL := resp.Request.URL
	article, err := readability.FromReader(strings.NewReader(string(data)), finalURL)
	var title, text string
	if err == nil {
		title = strings.TrimSpace(article.Title)
		text = cleanText(article.TextContent)
	}
	fallbackTitle, fallbackText := fallbackHTMLText(string(data))
	if fallbackTitle != "" {
		title = fallbackTitle
	}
	if countRunes(text) < minReadableRunes {
		if title == "" {
			title = fallbackTitle
		}
		text = fallbackText
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("no readable text found on webpage")
	}
	if countRunes(text) > maxArticleRunes {
		return nil, fmt.Errorf("webpage text exceeds 500,000 characters (%d characters)", countRunes(text))
	}
	if title == "" {
		title = finalURL.Hostname()
	}

	return &Article{URL: finalURL.String(), Title: title, Text: text}, nil
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
			return validateWebDestination(req.Context(), req.URL, false)
		},
	}
	if allowPrivateDial {
		return client
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = nil
	originalDialContext := transport.DialContext
	if originalDialContext == nil {
		originalDialContext = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := publicWebIPs(ctx, host, false)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := originalDialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("resolving webpage host %q: no public IP addresses", host)
	}

	client.Transport = transport
	return client
}

func validateWebDestination(ctx context.Context, u *url.URL, allowPrivate bool) error {
	if u == nil || u.Host == "" {
		return errors.New("invalid webpage URL")
	}
	_, err := publicWebIPs(ctx, u.Hostname(), allowPrivate)
	return err
}

func publicWebIPs(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	if host == "" {
		return nil, errors.New("invalid webpage URL")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !allowPrivate && isPrivateWebAddr(addr) {
			return nil, fmt.Errorf("webpage destination is a private network address: %s", addr)
		}
		return []netip.Addr{addr}, nil
	}

	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolving webpage host %q: %w", host, err)
	}
	publicIPs := make([]netip.Addr, 0, len(ips))
	for _, addr := range ips {
		if isPrivateWebAddr(addr) {
			if !allowPrivate {
				return nil, fmt.Errorf("webpage destination resolves to private network address: %s", addr)
			}
			publicIPs = append(publicIPs, addr)
			continue
		}
		publicIPs = append(publicIPs, addr)
	}
	if len(publicIPs) == 0 {
		return nil, fmt.Errorf("resolving webpage host %q: no IP addresses", host)
	}
	return publicIPs, nil
}

func isPrivateWebAddr(addr netip.Addr) bool {
	if !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() {
		return true
	}
	for _, prefix := range privateWebPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func validateURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("webpage URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid webpage URL: %s", rawURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported webpage URL scheme %q; use http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid webpage URL: %s", rawURL)
	}
	return parsed, nil
}

func fallbackHTMLText(rawHTML string) (string, string) {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", ""
	}

	var titleParts, h1Parts, textParts []string
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, skip bool) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "svg", "canvas", "iframe", "nav", "footer", "header":
				skip = true
			case "title":
				titleParts = append(titleParts, nodeText(n))
			case "h1":
				h1Parts = append(h1Parts, nodeText(n))
			}
		}
		if !skip && n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				textParts = append(textParts, text)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, skip)
		}
	}
	walk(doc, false)

	title := cleanText(strings.Join(h1Parts, " "))
	if title == "" {
		title = cleanText(strings.Join(titleParts, " "))
	}
	return title, cleanText(strings.Join(textParts, " "))
}

func nodeText(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			if text := strings.TrimSpace(cur.Data); text != "" {
				parts = append(parts, text)
			}
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, " ")
}

func cleanText(value string) string {
	lines := strings.Fields(value)
	return strings.TrimSpace(strings.Join(lines, " "))
}

func countRunes(value string) int {
	count := 0
	for range value {
		count++
	}
	return count
}
