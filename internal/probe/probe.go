// Package probe извлекает технические параметры медиафайлов через ffprobe.
package probe

import (
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// MediaInfo — извлечённые параметры одного видеофайла.
type MediaInfo struct {
	Path       string
	Width      int
	Height     int
	FPS        float64
	Duration   float64 // длительность в секундах
	VideoCodec string
	AudioCodec string
	HasAudio   bool
}

// ffprobeOutput описывает релевантную часть JSON-вывода ffprobe.
type ffprobeOutput struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		RFrameRate   string `json:"r_frame_rate"`
		AvgFrameRate string `json:"avg_frame_rate"`
		Duration     string `json:"duration"`
		// SideDataList несёт матрицу поворота (Display Matrix) для повёрнутых роликов.
		SideDataList []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Tags struct {
			// Rotate — устаревший способ хранения поворота (в контейнерах mov/mp4).
			Rotate string `json:"rotate"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Inspect запускает ffprobe для файла и возвращает его параметры.
func Inspect(path string) (*MediaInfo, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe для %q: %w", path, err)
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("разбор вывода ffprobe для %q: %w", path, err)
	}

	info := &MediaInfo{Path: path}
	foundVideo := false
	var videoStreamDur float64
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			if foundVideo {
				continue // берём первый видеопоток
			}
			foundVideo = true
			// ffprobe отдаёт "закодированное" разрешение, без учёта поворота из
			// метаданных. Но ffmpeg при декодировании (в т.ч. в -filter_complex)
			// автоматически применяет поворот, поэтому в фильтр приходит кадр уже в
			// ориентации отображения. Чтобы целевой формат совпал с этим кадром,
			// при повороте на 90/270° меняем ширину и высоту местами.
			info.Width = s.Width
			info.Height = s.Height
			rotation := 0.0
			for _, sd := range s.SideDataList {
				if sd.Rotation != 0 {
					rotation = sd.Rotation
					break
				}
			}
			if rotation == 0 {
				rotation = parseRational(s.Tags.Rotate)
			}
			if isQuarterTurn(rotation) {
				info.Width, info.Height = s.Height, s.Width
			}
			info.VideoCodec = s.CodecName
			info.FPS = parseFPS(s.RFrameRate, s.AvgFrameRate)
			videoStreamDur = parseRational(s.Duration)
		case "audio":
			if !info.HasAudio {
				info.HasAudio = true
				info.AudioCodec = s.CodecName
			}
		}
	}

	// Длительность: предпочитаем format.duration, иначе длительность видеопотока.
	info.Duration = parseRational(parsed.Format.Duration)
	if info.Duration <= 0 {
		info.Duration = videoStreamDur
	}

	if !foundVideo {
		return nil, fmt.Errorf("в файле %q не найден видеопоток", path)
	}
	if info.Width <= 0 || info.Height <= 0 {
		return nil, fmt.Errorf("не удалось определить разрешение для %q", path)
	}
	return info, nil
}

// isQuarterTurn сообщает, поворачивает ли ролик кадр на 90° или 270° (в любую
// сторону), из-за чего ширина и высота отображения меняются местами. Значение —
// в градусах; допускается небольшая погрешность и любой знак/период.
func isQuarterTurn(deg float64) bool {
	deg = math.Mod(math.Abs(deg), 180)
	return math.Abs(deg-90) < 1
}

// parseFPS разбирает значение вида "30000/1001" или "25/1" в число кадров в секунду.
// Если r_frame_rate непригоден, пробует avg_frame_rate; при неудаче возвращает 0.
func parseFPS(values ...string) float64 {
	for _, v := range values {
		if fps := parseRational(v); fps > 0 {
			return fps
		}
	}
	return 0
}

func parseRational(v string) float64 {
	v = strings.TrimSpace(v)
	if v == "" || v == "0/0" {
		return 0
	}
	if num, den, ok := strings.Cut(v, "/"); ok {
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if err1 == nil && err2 == nil && d != 0 {
			return n / d
		}
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}
