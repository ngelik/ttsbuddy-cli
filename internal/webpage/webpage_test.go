package webpage

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	article, err := FetchArticle(context.Background(), srv.URL, "test")
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
