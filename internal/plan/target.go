// Package plan выбирает целевые параметры вывода (кодеки, разрешение, fps)
// на основе анализа входных файлов.
package plan

import (
	"math"
	"strconv"

	"video_utils/internal/probe"
)

// Target — выбранные параметры итогового файла.
type Target struct {
	Width        int
	Height       int
	FPS          float64
	VideoCodec   string // имя кодека (h264, hevc, ...)
	AudioCodec   string // имя кодека (aac, mp3, ...)
	VideoEncoder string // энкодер ffmpeg (libx264, ...)
	AudioEncoder string // энкодер ffmpeg (aac, libmp3lame, ...)
	Container    string // mp4, webm, mkv
	Ext          string // .mp4, .webm, .mkv
}

// Ранги совместимости: больше — совместимее/предпочтительнее.
var videoCompat = map[string]int{
	"h264":  100,
	"mpeg4": 80,
	"hevc":  60,
	"vp9":   50,
	"vp8":   45,
	"av1":   40,
}

var audioCompat = map[string]int{
	"aac":    100,
	"mp3":    90,
	"ac3":    70,
	"opus":   60,
	"vorbis": 50,
}

// Маппинг кодек -> энкодер ffmpeg.
var videoEncoders = map[string]string{
	"h264":  "libx264",
	"hevc":  "libx265",
	"av1":   "libsvtav1",
	"vp9":   "libvpx-vp9",
	"vp8":   "libvpx",
	"mpeg4": "mpeg4",
}

var audioEncoders = map[string]string{
	"aac":    "aac",
	"mp3":    "libmp3lame",
	"ac3":    "ac3",
	"opus":   "libopus",
	"vorbis": "libvorbis",
}

// Контейнер по видеокодеку.
func containerFor(videoCodec string) (container, ext string) {
	switch videoCodec {
	case "vp9", "vp8":
		return "webm", ".webm"
	default:
		return "mp4", ".mp4"
	}
}

// ChooseTarget вычисляет целевые параметры по набору входных файлов.
// Разрешение и fps — максимальные среди входов; кодеки — самые совместимые
// среди встретившихся (с приоритетом h264/aac), fallback на h264/aac.
func ChooseTarget(infos []*probe.MediaInfo) Target {
	var t Target

	bestVideoRank := -1
	bestAudioRank := -1

	for _, in := range infos {
		if in.Width > t.Width {
			t.Width = in.Width
		}
		if in.Height > t.Height {
			t.Height = in.Height
		}
		if in.FPS > t.FPS {
			t.FPS = in.FPS
		}
		if r, ok := videoCompat[in.VideoCodec]; ok && r > bestVideoRank {
			bestVideoRank = r
			t.VideoCodec = in.VideoCodec
		}
		if in.HasAudio {
			if r, ok := audioCompat[in.AudioCodec]; ok && r > bestAudioRank {
				bestAudioRank = r
				t.AudioCodec = in.AudioCodec
			}
		}
	}

	// Fallback на максимально совместимые кодеки.
	if t.VideoCodec == "" {
		t.VideoCodec = "h264"
	}
	if t.AudioCodec == "" {
		t.AudioCodec = "aac"
	}

	// vp9/webm не поддерживает aac корректно -> для webm переключаем аудио на opus.
	container, ext := containerFor(t.VideoCodec)
	if container == "webm" && t.AudioCodec != "opus" && t.AudioCodec != "vorbis" {
		t.AudioCodec = "opus"
	}

	t.VideoEncoder = videoEncoders[t.VideoCodec]
	if t.VideoEncoder == "" {
		t.VideoEncoder = "libx264"
	}
	t.AudioEncoder = audioEncoders[t.AudioCodec]
	if t.AudioEncoder == "" {
		t.AudioEncoder = "aac"
	}
	t.Container = container
	t.Ext = ext

	// Подстраховки на случай нулей.
	if t.FPS <= 0 {
		t.FPS = 30
	}
	// Чётные размеры — требование большинства энкодеров (yuv420p).
	t.Width = makeEven(t.Width)
	t.Height = makeEven(t.Height)

	return t
}

func makeEven(n int) int {
	if n%2 != 0 {
		return n + 1
	}
	return n
}

// FPSString форматирует fps без лишних нулей для использования в фильтрах ffmpeg.
func (t Target) FPSString() string {
	if t.FPS == math.Trunc(t.FPS) {
		return strconv.Itoa(int(t.FPS))
	}
	return strconv.FormatFloat(t.FPS, 'f', 3, 64)
}
