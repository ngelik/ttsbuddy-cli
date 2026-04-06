package markdown

import "testing"

func TestStripHeadings(t *testing.T) {
	got := Strip("# Hello\n## World\n### Three")
	want := "Hello\nWorld\nThree"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripBoldItalic(t *testing.T) {
	got := Strip("This is **bold** and *italic* and ***both***.")
	want := "This is bold and italic and both."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripLinksKeepText(t *testing.T) {
	got := Strip("[Click here](https://example.com)")
	want := "Click here"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripSnakeCasePreserved(t *testing.T) {
	got := Strip("Use the snake_case_variable in my_function_name.")
	if !containsStr(got, "snake_case_variable") {
		t.Errorf("snake_case should be preserved: got %q", got)
	}
	if !containsStr(got, "my_function_name") {
		t.Errorf("my_function_name should be preserved: got %q", got)
	}
}

func TestStripImages(t *testing.T) {
	got := Strip("![alt text](https://example.com/img.png)")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestStripBareURLs(t *testing.T) {
	got := Strip("Visit https://example.com for more.")
	want := "Visit  for more."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripInlineCode(t *testing.T) {
	got := Strip("Use `fmt.Println` to print.")
	want := "Use fmt.Println to print."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripCodeFence(t *testing.T) {
	input := "Before\n```go\nfmt.Println(\"hello\")\n```\nAfter"
	got := Strip(input)
	want := "Before\n\nAfter"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripHTML(t *testing.T) {
	got := Strip("Hello <b>world</b> and <a href='url'>link</a>.")
	want := "Hello world and link."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripAngleBracketsPreserved(t *testing.T) {
	got := Strip("If 1 < 2 and 3 > 1, then math works.")
	want := "If 1 < 2 and 3 > 1, then math works."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripStandaloneBullets(t *testing.T) {
	got := Strip("line1\n•\nline2\n-\nline3")
	want := "line1\n\nline2\n\nline3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripCollapseNewlines(t *testing.T) {
	got := Strip("a\n\n\n\nb")
	want := "a\n\nb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripPlainTextPassthrough(t *testing.T) {
	input := "This is plain text with no markdown."
	got := Strip(input)
	if got != input {
		t.Errorf("plain text should pass through unchanged: got %q", got)
	}
}

func TestStripEmpty(t *testing.T) {
	got := Strip("")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestStripCombinedDocument(t *testing.T) {
	input := `# Title

This is **bold** text with a [link](https://example.com).

![image](https://example.com/img.png)

## Section

- Item one
- Item two

Visit https://example.com for details.

` + "```" + `
code block
` + "```" + `

End.`

	got := Strip(input)

	// Should contain "Title", "link" text, "Section", items, "End"
	// Should NOT contain "#", "**", "[", "](", "![", "https://", "```"
	for _, bad := range []string{"#", "**", "](", "![", "```"} {
		if containsStr(got, bad) {
			t.Errorf("output should not contain %q:\n%s", bad, got)
		}
	}
	// Link text should be preserved
	if !containsStr(got, "link") {
		t.Errorf("link text should be preserved:\n%s", got)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
