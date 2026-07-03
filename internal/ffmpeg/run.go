package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// EnsureTools проверяет наличие ffmpeg и ffprobe в PATH.
func EnsureTools() error {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s не найден в PATH: %w", tool, err)
		}
	}
	return nil
}

// baseArgs — общие флаги, подавляющие лишний вывод ffmpeg.
func baseArgs() []string {
	return []string{"-y", "-hide_banner", "-nostdin", "-loglevel", "error"}
}

// Run запускает ffmpeg, не выводя его прогресс. Вывод ffmpeg перехватывается и
// показывается (хвост) только при ошибке.
func Run(args []string) error {
	return RunContext(context.Background(), args)
}

// RunContext — как Run, но прерывается при отмене контекста (для параллельной
// обработки: отмена останавливает ещё работающие процессы ffmpeg).
func RunContext(ctx context.Context, args []string) error {
	full := append(baseArgs(), args...)
	cmd := exec.CommandContext(ctx, "ffmpeg", full...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // отменено — не засоряем вывод хвостом ffmpeg
		}
		return fmt.Errorf("ffmpeg завершился с ошибкой: %w\n%s", err, tail(buf.String(), 15))
	}
	return nil
}

// FullCommand возвращает читаемое представление команды (для --dry-run).
func FullCommand(args []string) string {
	full := append([]string{"ffmpeg"}, baseArgs()...)
	full = append(full, args...)
	parts := make([]string, len(full))
	for i, a := range full {
		if strings.ContainsAny(a, " \t") {
			parts[i] = `"` + a + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// tail возвращает последние n непустых строк текста.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return strings.Join(out, "\n")
}
