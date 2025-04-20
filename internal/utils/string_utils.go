package utils

import "unicode/utf8"

// TruncateString обрезает строку до указанной максимальной длины в рунах,
// добавляя "..." в конце, если строка была обрезана.
// Обрабатывает nil строки и отрицательную длину.
func TruncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		// Недостаточно места даже для "..."
		runes := []rune(s)
		return string(runes[:maxLen])
	}
	runes := []rune(s)
	return string(runes[:maxLen-3]) + "..."
}

// TruncateStringEnd обрезает строку до максимальной длины в рунах без добавления "..."
func TruncateStringEnd(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen])
}
