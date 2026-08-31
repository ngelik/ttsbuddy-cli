package units

import "unicode/utf16"

// UTF16Units mirrors the server's JavaScript string.length billing contract.
func UTF16Units(text string) int {
	return len(utf16.Encode([]rune(text)))
}
