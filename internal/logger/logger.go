// Package logger выводит сообщения с цветовым выделением:
// ошибки — красным, предупреждения — зелёным, обычные сообщения — без цвета.
package logger

import (
	"fmt"
	"os"
)

const (
	colorRed   = "\x1b[31m"
	colorGreen = "\x1b[32m"
	colorReset = "\x1b[0m"
)

// colorEnabled управляет выводом ANSI-кодов (отключается переменной NO_COLOR).
var colorEnabled = os.Getenv("NO_COLOR") == ""

func colorize(c, s string) string {
	if !colorEnabled {
		return s
	}
	return c + s + colorReset
}

// Info печатает обычное информационное сообщение (без цвета) в stdout.
func Info(format string, a ...any) {
	fmt.Fprintln(os.Stdout, fmt.Sprintf(format, a...))
}

// Warn печатает предупреждение зелёным в stderr.
func Warn(format string, a ...any) {
	fmt.Fprintln(os.Stderr, colorize(colorGreen, fmt.Sprintf(format, a...)))
}

// Error печатает ошибку красным в stderr.
func Error(format string, a ...any) {
	fmt.Fprintln(os.Stderr, colorize(colorRed, fmt.Sprintf(format, a...)))
}
