// Package config загружает и валидирует YAML-конфигурацию утилиты склейки видео.
package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Значения по умолчанию.
const (
	DefaultSplashSeconds = 2.0
	DefaultBackground    = "black"
	DefaultTextColor     = "white"
)

// Options — глобальные опции склейки из секции `options` YAML-файла.
type Options struct {
	// SplashSeconds — длительность текстовой заставки в секундах.
	SplashSeconds float64 `yaml:"splash_seconds"`
	// Font — путь к TTF/OTF-шрифту для заставки. Пусто -> системный шрифт по ОС.
	Font string `yaml:"font"`
	// Background — цвет фона заставки (имя ffmpeg-цвета или 0xRRGGBB).
	Background string `yaml:"background"`
	// TextColor — цвет текста заставки.
	TextColor string `yaml:"text_color"`
	// Jobs — число одновременно кодируемых сегментов (0 = авто: CPU/2).
	Jobs int `yaml:"jobs"`
	// TempDir — каталог для временных файлов (пусто -> системный временный каталог).
	TempDir string `yaml:"temp_dir"`
}

// VideoItem — элемент списка видео.
type VideoItem struct {
	// Name — заголовок. Если задан, перед видео вставляется заставка с этим текстом.
	Name string `yaml:"name"`
	// Path — путь к видеофайлу (обязателен).
	Path string `yaml:"path"`
	// FitDuration — целевая длительность в формате "MM:SS" или "HH:MM:SS". Если
	// задана и меньше исходной длительности, видео ускоряется так, чтобы полностью
	// уложиться в это время (аудио при этом заменяется тишиной). Пусто — без ускорения.
	FitDuration string `yaml:"fit_duration"`
	// ShowOriginalTime — если true, поверх видео выводится исходное время от начала
	// ролика (HH:MM:SS, идущее в оригинальном темпе) в левом верхнем углу.
	ShowOriginalTime bool `yaml:"show_original_time"`
}

// Config — корневая структура конфигурации.
type Config struct {
	// Output — путь к итоговому файлу.
	Output  string      `yaml:"output"`
	Options Options     `yaml:"options"`
	Videos  []VideoItem `yaml:"videos"`
}

// Load читает YAML-файл, применяет значения по умолчанию и валидирует конфигурацию.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать конфиг %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("не удалось разобрать YAML %q: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Options.SplashSeconds <= 0 {
		c.Options.SplashSeconds = DefaultSplashSeconds
	}
	if c.Options.Background == "" {
		c.Options.Background = DefaultBackground
	}
	if c.Options.TextColor == "" {
		c.Options.TextColor = DefaultTextColor
	}
	if c.Options.Font == "" {
		c.Options.Font = defaultFont()
	}
}

func (c *Config) validate() error {
	if len(c.Videos) == 0 {
		return fmt.Errorf("список videos пуст — нечего склеивать")
	}
	if c.Output == "" {
		return fmt.Errorf("не указан output (путь к итоговому файлу)")
	}
	if c.Options.Jobs < 0 {
		return fmt.Errorf("options.jobs не может быть отрицательным: %d", c.Options.Jobs)
	}
	for i, v := range c.Videos {
		if v.Path == "" {
			return fmt.Errorf("videos[%d]: не указан path", i)
		}
		if _, err := os.Stat(v.Path); err != nil {
			return fmt.Errorf("videos[%d] (%q): файл недоступен: %w", i, v.Path, err)
		}
		if v.FitDuration != "" {
			if _, err := ParseTimecode(v.FitDuration); err != nil {
				return fmt.Errorf("videos[%d] (%q): некорректный fit_duration: %w", i, v.Path, err)
			}
		}
	}
	return nil
}

// ParseTimecode разбирает длительность в формате "MM:SS" или "HH:MM:SS" в секунды.
// Формат без двоеточия не допускается.
func ParseTimecode(s string) (float64, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("ожидается формат MM:SS или HH:MM:SS, получено %q", s)
	}
	nums := make([]float64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("некорректное числовое поле %q в %q", p, s)
		}
		nums[i] = n
	}
	var total float64
	for _, n := range nums {
		total = total*60 + n
	}
	if total <= 0 {
		return 0, fmt.Errorf("длительность должна быть больше нуля: %q", s)
	}
	return total, nil
}

// defaultFont возвращает разумный системный шрифт по умолчанию для текущей ОС.
func defaultFont() string {
	switch runtime.GOOS {
	case "windows":
		return `C:/Windows/Fonts/arial.ttf`
	case "darwin":
		return "/System/Library/Fonts/Supplemental/Arial.ttf"
	default: // linux и прочие
		return "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
	}
}
