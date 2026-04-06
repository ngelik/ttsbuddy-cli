package markdown

import (
	"regexp"
	"strings"
)

var (
	reHTML           = regexp.MustCompile(`<[^>]+>`)
	reImage          = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
	reLink           = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reBareURL        = regexp.MustCompile(`(?i)\bhttps?://\S+`)
	reHeading        = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reBold3Star  = regexp.MustCompile(`\*{3}(.+?)\*{3}`)
	reBold2Star  = regexp.MustCompile(`\*{2}(.+?)\*{2}`)
	reItalicStar = regexp.MustCompile(`\*(.+?)\*`)
	reBold3Under = regexp.MustCompile(`_{3}(.+?)_{3}`)
	reBold2Under = regexp.MustCompile(`_{2}(.+?)_{2}`)
	reItalicUnder = regexp.MustCompile(`_(.+?)_`)
	reInlineCode     = regexp.MustCompile("`([^`]+)`")
	reCodeFence      = regexp.MustCompile("(?s)```[\\s\\S]*?```")
	reStandaloneBullet = regexp.MustCompile(`(?m)^[•\-\*]\s*$`)
	reTripleNewline  = regexp.MustCompile(`\n{3,}`)
)

// Strip removes markdown syntax from text, preserving link text for narration.
// Designed for TTS preprocessing — differs from server-side basicMarkdownToText()
// which drops both link text and URL.
func Strip(input string) string {
	s := input

	// Order matters: remove complex structures first, then inline markers.
	s = reCodeFence.ReplaceAllString(s, "")       // Code fences before inline code
	s = reHTML.ReplaceAllString(s, "")             // HTML tags
	s = reImage.ReplaceAllString(s, "")            // Images (before links)
	s = reLink.ReplaceAllString(s, "$1")           // Links: keep text, drop URL
	s = reBareURL.ReplaceAllString(s, "")          // Bare URLs
	s = reHeading.ReplaceAllString(s, "")          // Heading markers
	s = reBold3Star.ReplaceAllString(s, "$1")       // ***bold italic***
	s = reBold2Star.ReplaceAllString(s, "$1")       // **bold**
	s = reItalicStar.ReplaceAllString(s, "$1")      // *italic*
	s = reBold3Under.ReplaceAllString(s, "$1")      // ___bold italic___
	s = reBold2Under.ReplaceAllString(s, "$1")       // __bold__
	s = reItalicUnder.ReplaceAllString(s, "$1")      // _italic_
	s = reInlineCode.ReplaceAllString(s, "$1")     // Inline code backticks
	s = reStandaloneBullet.ReplaceAllString(s, "") // Standalone bullet markers
	s = reTripleNewline.ReplaceAllString(s, "\n\n") // Collapse excessive newlines

	return strings.TrimSpace(s)
}
