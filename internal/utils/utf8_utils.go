package utils

import (
	"strings"
	"unicode/utf8"
)

// SanitizeUTF8 заменяет невалидные UTF-8 байты в строке на символ замены Unicode (U+FFFD).
func SanitizeUTF8(s string) string {
	// Используем strings.ToValidUTF8 для простой и эффективной очистки.
	// Второй аргумент - строка, которая будет вставлена вместо невалидных последовательностей.
	// Используем utf8.RuneError (�), чтобы визуально обозначить проблемные места.
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}
