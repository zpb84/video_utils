// Package ffmpeg собирает аргументы команд ffmpeg и запускает их.
//
// Склейка выполняется в два этапа:
//  1. Каждый сегмент (заставка и видео) по отдельности кодируется в общий
//     целевой формат во временный файл — это позволяет показывать прогресс и
//     время обработки по каждому файлу.
//  2. Полученные одинаковые по параметрам сегменты быстро склеиваются
//     concat-демультиплексором с потоковым копированием (без перекодирования).
package ffmpeg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"video_utils/internal/config"
	"video_utils/internal/plan"
	"video_utils/internal/probe"
)

const (
	audioSampleRate    = 48000
	audioChannelLayout = "stereo"
)

// SplashArgs формирует аргументы ffmpeg для рендеринга одной текстовой заставки
// (цветной фон + текст из textPath + тишина) во временный файл out.
func SplashArgs(cfg *config.Config, t plan.Target, textPath, out string) []string {
	size := fmt.Sprintf("%dx%d", t.Width, t.Height)
	fps := t.FPSString()
	dur := strconv.FormatFloat(cfg.Options.SplashSeconds, 'f', -1, 64)

	filter := fmt.Sprintf(
		"[0:v]drawtext=fontfile=%s:textfile=%s:fontcolor=%s:fontsize=h/15:"+
			"x=(w-text_w)/2:y=(h-text_h)/2:expansion=none,format=yuv420p,setsar=1[v];"+
			"[1:a]%s[a]",
		escapeFilterPath(cfg.Options.Font), escapeFilterPath(textPath),
		cfg.Options.TextColor, audioNormChain(),
	)

	args := []string{
		"-f", "lavfi", "-t", dur, "-i", fmt.Sprintf("color=c=%s:s=%s:r=%s", cfg.Options.Background, size, fps),
		"-f", "lavfi", "-t", dur, "-i", fmt.Sprintf("anullsrc=r=%d:cl=%s", audioSampleRate, audioChannelLayout),
		"-filter_complex", filter,
		"-map", "[v]", "-map", "[a]",
	}
	args = append(args, videoEncodeArgs(t)...)
	args = append(args, audioEncodeArgs(t)...)
	args = append(args, out)
	return args
}

// VideoArgs формирует аргументы ffmpeg для приведения одного видеофайла к
// целевому формату (масштаб + letterbox, общий fps/кодек/аудио) во временный
// файл out. Для роликов без аудио добавляется дорожка тишины нужной длины.
//
// speed > 1 ускоряет ролик так, чтобы он уложился в целевую длительность
// (setpts=PTS/speed); при ускорении аудио заменяется тишиной новой длины.
// Если у item задан ShowOriginalTime, поверх видео выводится исходное время от
// начала ролика (до применения setpts, поэтому таймкод идёт в оригинальном темпе).
func VideoArgs(cfg *config.Config, item config.VideoItem, info *probe.MediaInfo, t plan.Target, out string, speed float64) []string {
	fps := t.FPSString()

	args := []string{"-i", item.Path}

	// Видеоцепочка собирается по шагам: масштаб + letterbox, затем опциональная
	// накладка исходного времени и ускорение, и в конце — общий fps и pix_fmt.
	vSteps := []string{
		fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease", t.Width, t.Height),
		fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", t.Width, t.Height),
		"setsar=1",
	}
	if item.ShowOriginalTime {
		// Нормализуем старт таймкода к нулю, затем рисуем время оригинального ролика.
		// drawtext стоит до setpts=PTS/speed, поэтому показывает исходное время.
		vSteps = append(vSteps, "setpts=PTS-STARTPTS", originalTimeOverlay(cfg))
	}
	if speed > 1 {
		vSteps = append(vSteps, "setpts=PTS/"+strconv.FormatFloat(speed, 'f', -1, 64))
	}
	vSteps = append(vSteps, "fps="+fps, "format=yuv420p")
	vFilter := strings.Join(vSteps, ",") + "[v]"

	// При ускорении звук заменяется тишиной новой (укороченной) длины; иначе —
	// исходное аудио, а для роликов без звука — тишина исходной длины.
	muted := speed > 1
	var aFilter string
	if info.HasAudio && !muted {
		aFilter = fmt.Sprintf("[0:a]%s[a]", audioNormChain())
	} else {
		dur := info.Duration
		if muted && speed > 0 {
			dur = info.Duration / speed
		}
		if dur <= 0 {
			dur = 1
		}
		args = append(args,
			"-f", "lavfi", "-t", strconv.FormatFloat(dur, 'f', 3, 64),
			"-i", fmt.Sprintf("anullsrc=r=%d:cl=%s", audioSampleRate, audioChannelLayout),
		)
		aFilter = fmt.Sprintf("[1:a]%s[a]", audioNormChain())
	}

	args = append(args,
		"-filter_complex", vFilter+";"+aFilter,
		"-map", "[v]", "-map", "[a]",
	)
	args = append(args, videoEncodeArgs(t)...)
	args = append(args, audioEncodeArgs(t)...)
	args = append(args, out)
	return args
}

// originalTimeOverlay возвращает шаг drawtext, выводящий исходное время от начала
// ролика в формате HH:MM:SS в левом верхнем углу (полупрозрачная подложка для
// читаемости).
//
// Экранирование значения text проходит два уровня разбора внутри одинарных кавычек:
//   - двоеточия-разделители в %{pts:gmtime:0:...} пишутся как "\:" (внутри кавычек
//     развернётся в литеральное ":", по которому делит аргументы парсер %{...});
//   - двоеточия внутри strftime-формата %H:%M:%S пишутся как "\\\:" (развернётся в
//     "\:", чтобы парсер %{...} оставил их литеральными и не счёл лишними аргументами).
//
// Порядок бэкслэшей проверен эмпирически на используемой сборке ffmpeg: результат
// должен дать в filtergraph строку text='%{pts\:gmtime\:0\:%H\\\:%M\\\:%S}'.
func originalTimeOverlay(cfg *config.Config) string {
	return fmt.Sprintf(
		"drawtext=fontfile=%s:text='%%{pts\\:gmtime\\:0\\:%%H\\\\\\:%%M\\\\\\:%%S}':"+
			"fontcolor=%s:fontsize=h/15:x=10:y=10:box=1:boxcolor=black@0.5:boxborderw=5",
		escapeFilterPath(cfg.Options.Font), cfg.Options.TextColor,
	)
}

// WriteConcatList пишет файл-список для concat-демультиплексора и возвращает путь.
func WriteConcatList(dir string, segments []string) (string, error) {
	listPath := filepath.Join(dir, "concat.txt")
	var b strings.Builder
	for _, s := range segments {
		p := strings.ReplaceAll(s, `\`, `/`)
		p = strings.ReplaceAll(p, `'`, `'\''`)
		fmt.Fprintf(&b, "file '%s'\n", p)
	}
	if err := os.WriteFile(listPath, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("запись списка склейки: %w", err)
	}
	return listPath, nil
}

// ConcatArgs формирует аргументы финальной склейки сегментов копированием потоков.
func ConcatArgs(listPath, out string, t plan.Target) []string {
	args := []string{"-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy"}
	if t.Container == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	return args
}

// audioNormChain — цепочка приведения аудио к общему формату.
func audioNormChain() string {
	return fmt.Sprintf(
		"aformat=sample_rates=%d:channel_layouts=%s,aresample=async=1:first_pts=0",
		audioSampleRate, audioChannelLayout,
	)
}

// videoEncodeArgs возвращает параметры видеоэнкодера под выбранный кодек.
func videoEncodeArgs(t plan.Target) []string {
	switch t.VideoEncoder {
	case "libx264":
		return []string{"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p"}
	case "libx265":
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", "24", "-pix_fmt", "yuv420p", "-tag:v", "hvc1"}
	case "libsvtav1":
		return []string{"-c:v", "libsvtav1", "-crf", "30", "-preset", "6", "-pix_fmt", "yuv420p"}
	case "libvpx-vp9":
		return []string{"-c:v", "libvpx-vp9", "-crf", "30", "-b:v", "0", "-pix_fmt", "yuv420p"}
	case "libvpx":
		return []string{"-c:v", "libvpx", "-crf", "10", "-b:v", "1M", "-pix_fmt", "yuv420p"}
	case "mpeg4":
		return []string{"-c:v", "mpeg4", "-q:v", "4", "-pix_fmt", "yuv420p"}
	default:
		return []string{"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p"}
	}
}

// audioEncodeArgs возвращает параметры аудиоэнкодера под выбранный кодек.
func audioEncodeArgs(t plan.Target) []string {
	switch t.AudioEncoder {
	case "aac":
		return []string{"-c:a", "aac", "-b:a", "192k"}
	case "libmp3lame":
		return []string{"-c:a", "libmp3lame", "-q:a", "2"}
	case "ac3":
		return []string{"-c:a", "ac3", "-b:a", "192k"}
	case "libopus":
		return []string{"-c:a", "libopus", "-b:a", "160k"}
	case "libvorbis":
		return []string{"-c:a", "libvorbis", "-q:a", "5"}
	default:
		return []string{"-c:a", "aac", "-b:a", "192k"}
	}
}

// escapeFilterPath готовит путь (к шрифту или textfile) для опции внутри
// filtergraph ffmpeg: прямые слэши и двойное экранирование двоеточия диска.
// Значение проходит два уровня разбора (filtergraph + опции фильтра), поэтому
// двоеточие в "C:/..." экранируется как "\\:" (C\\:/...).
func escapeFilterPath(p string) string {
	p = strings.ReplaceAll(p, `\`, `/`)
	p = strings.ReplaceAll(p, `:`, `\\:`)
	return p
}
