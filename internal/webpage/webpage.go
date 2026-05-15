package webpage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// Article is readable webpage content ready to submit for TTS.
type Article struct {
	URL   string
	Title string
	Text  string
}

// FetchArticle fetches a public HTTP(S) page and extracts readable article text.
func FetchArticle(ctx context.Context, rawURL, version string) (*Article, error) {
	parsed, err := validateURL(rawURL)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects while fetching webpage")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirected to unsupported URL scheme %q; use http or https", req.URL.Scheme)
			}
			return nil
		},
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
