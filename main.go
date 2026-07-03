// Command video_utils склеивает список видеофайлов из YAML-конфига
// в один файл через ffmpeg, опционально вставляя текстовые заставки.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"video_utils/internal/config"
	"video_utils/internal/ffmpeg"
	"video_utils/internal/logger"
	"video_utils/internal/plan"
	"video_utils/internal/probe"
)

// segJob — независимое задание кодирования одного сегмента (заставка или видео).
type segJob struct {
	order int      // позиция в итоговой склейке
	label string   // отображаемое имя для логов
	out   string   // путь временного файла-результата
	args  []string // аргументы ffmpeg
}

func main() {
	if err := run(); err != nil {
		logger.Error("Ошибка: %v", err)
		os.Exit(1)
	}
}

func run() error {
	runStart := time.Now()

	configPath := flag.String("c", "", "путь к YAML-конфигу (обязательно)")
	outputOverride := flag.String("o", "", "переопределить путь к итоговому файлу")
	jobsFlag := flag.Int("j", 0, "число одновременно кодируемых сегментов (0 = авто: CPU/2)")
	dryRun := flag.Bool("dry-run", false, "показать команды ffmpeg и выйти")
	verbose := flag.Bool("v", false, "подробный вывод")
	flag.Parse()

	if *configPath == "" {
		flag.Usage()
		return fmt.Errorf("не указан флаг -c <config.yaml>")
	}

	if err := ffmpeg.EnsureTools(); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	output := cfg.Output
	if *outputOverride != "" {
		output = *outputOverride
	}

	// Пробируем все входные файлы.
	infos := make([]*probe.MediaInfo, len(cfg.Videos))
	for i, v := range cfg.Videos {
		info, err := probe.Inspect(v.Path)
		if err != nil {
			return err
		}
		infos[i] = info
		if *verbose {
			logger.Info("[probe] %s: %dx%d @ %.3ffps, video=%s, audio=%s(%v), dur=%.2fs",
				v.Path, info.Width, info.Height, info.FPS,
				info.VideoCodec, info.AudioCodec, info.HasAudio, info.Duration)
		}
	}

	target := plan.ChooseTarget(infos)

	// Согласуем расширение output с выбранным контейнером.
	if adjusted := adjustExtension(output, target.Ext); adjusted != output {
		logger.Warn("Расширение итогового файла изменено на %s (контейнер %s): %s",
			target.Ext, target.Container, adjusted)
		output = adjusted
	}

	if *verbose || *dryRun {
		logger.Info("[target] %dx%d @ %sfps, video=%s(%s), audio=%s(%s), container=%s -> %s",
			target.Width, target.Height, target.FPSString(),
			target.VideoCodec, target.VideoEncoder,
			target.AudioCodec, target.AudioEncoder, target.Container, output)
	}

	// Временный каталог для сегментов.
	tmpBase := cfg.Options.TempDir
	if tmpBase != "" {
		if err := os.MkdirAll(tmpBase, 0o755); err != nil {
			return fmt.Errorf("создание каталога для временных файлов %q: %w", tmpBase, err)
		}
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "vc_")
	if err != nil {
		return fmt.Errorf("создание временного каталога: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	// Абсолютный путь нужен, чтобы concat-демультиплексор корректно открывал
	// сегменты (относительные пути он трактует относительно файла-списка).
	if abs, err := filepath.Abs(tmpDir); err == nil {
		tmpDir = abs
	}
	if *verbose {
		logger.Info("[temp] временный каталог: %s", tmpDir)
	}

	// Фаза подготовки: строим плоский список заданий в порядке склейки.
	jobs, err := buildJobs(cfg, infos, target, tmpDir)
	if err != nil {
		return err
	}

	// --dry-run: печатаем команды по порядку и выходим.
	if *dryRun {
		for _, j := range jobs {
			logger.Info("%s", ffmpeg.FullCommand(j.args))
		}
		listPath := filepath.Join(tmpDir, "concat.txt")
		logger.Info("%s", ffmpeg.FullCommand(ffmpeg.ConcatArgs(listPath, output, target)))
		return nil
	}

	// Фаза кодирования: параллельно с ограничением concurrency.
	concurrency := min(resolveJobs(*jobsFlag, cfg.Options.Jobs), len(jobs))
	logger.Info("Кодирование %d сегментов (параллельно: %d)...", len(jobs), concurrency)

	segments := make([]string, len(jobs))
	if err := encodeJobs(jobs, segments, concurrency); err != nil {
		return err
	}

	// Фаза склейки: быстрое копирование потоков.
	logger.Info("Склейка %d сегментов...", len(segments))
	listPath, err := ffmpeg.WriteConcatList(tmpDir, segments)
	if err != nil {
		return err
	}
	if err := ffmpeg.Run(ffmpeg.ConcatArgs(listPath, output, target)); err != nil {
		return err
	}

	logger.Info("Готово: %s (полное время работы: %s)", output, formatDuration(time.Since(runStart)))
	return nil
}

// buildJobs формирует список заданий кодирования в порядке итоговой склейки.
func buildJobs(cfg *config.Config, infos []*probe.MediaInfo, target plan.Target, tmpDir string) ([]segJob, error) {
	var jobs []segJob
	for i, item := range cfg.Videos {
		info := infos[i]
		label := displayName(item)

		// Заставка перед видео.
		if item.Name != "" {
			textPath := filepath.Join(tmpDir, fmt.Sprintf("text_%03d.txt", i))
			if err := os.WriteFile(textPath, []byte(item.Name), 0o644); err != nil {
				return nil, fmt.Errorf("запись текста заставки: %w", err)
			}
			splashPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%03d_0splash%s", i, target.Ext))
			jobs = append(jobs, segJob{
				order: len(jobs),
				label: label + " (заставка)",
				out:   splashPath,
				args:  ffmpeg.SplashArgs(cfg, target, textPath, splashPath),
			})
		}

		// Скорость ускорения: укладываем ролик в целевую длительность fit_duration.
		speed := resolveSpeed(item, info, label)

		// Основной ролик. При ускорении аудио заменяется тишиной, поэтому про
		// отсутствие исходной дорожки предупреждаем только когда скорость не меняем.
		if !info.HasAudio && speed <= 1 {
			logger.Warn("%s: нет аудиодорожки — добавлена тишина", label)
		}
		videoPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%03d_1video%s", i, target.Ext))
		jobs = append(jobs, segJob{
			order: len(jobs),
			label: label,
			out:   videoPath,
			args:  ffmpeg.VideoArgs(cfg, item, info, target, videoPath, speed),
		})
	}
	return jobs, nil
}

// encodeJobs кодирует задания параллельно с ограничением concurrency и заполняет
// segments[order]. При первой ошибке отменяет остальные задания и возвращает её.
func encodeJobs(jobs []segJob, segments []string, concurrency int) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for _, j := range jobs {
		wg.Add(1)
		go func(j segJob) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			logger.Info("→ Обработка: %s", j.label)
			start := time.Now()
			if err := ffmpeg.RunContext(ctx, j.args); err != nil {
				if ctx.Err() == nil {
					logger.Error("✗ %s: %v", j.label, err)
				}
				once.Do(func() {
					firstErr = fmt.Errorf("обработка %q: %w", j.label, err)
					cancel()
				})
				return
			}
			segments[j.order] = j.out
			logger.Info("✓ Готово: %s (за %s)", j.label, formatDuration(time.Since(start)))
		}(j)
	}

	wg.Wait()
	return firstErr
}

// resolveSpeed вычисляет коэффициент ускорения ролика по item.FitDuration и его
// исходной длительности. Возвращает 1.0 (без ускорения), если fit_duration не задан,
// длительность неизвестна или цель не меньше исходной длины.
func resolveSpeed(item config.VideoItem, info *probe.MediaInfo, label string) float64 {
	if item.FitDuration == "" {
		return 1.0
	}
	// Значение уже проверено в config.validate, ошибка здесь маловероятна.
	tsec, err := config.ParseTimecode(item.FitDuration)
	if err != nil {
		logger.Warn("%s: некорректный fit_duration %q — ускорение пропущено", label, item.FitDuration)
		return 1.0
	}
	if info.Duration <= 0 {
		logger.Warn("%s: неизвестна длительность ролика — ускорение пропущено", label)
		return 1.0
	}
	if tsec >= info.Duration {
		logger.Warn("%s: целевое время %s не меньше исходного (%.2fс) — ускорение не применяется",
			label, item.FitDuration, info.Duration)
		return 1.0
	}
	speed := info.Duration / tsec
	logger.Info("%s: ускорение x%.2f (%.2fс → %s)", label, speed, info.Duration, item.FitDuration)
	return speed
}

// resolveJobs определяет степень параллелизма: флаг > опция конфига > авто (CPU/2).
func resolveJobs(flagJ, cfgJobs int) int {
	switch {
	case flagJ > 0:
		return flagJ
	case cfgJobs > 0:
		return cfgJobs
	default:
		if n := runtime.NumCPU() / 2; n > 0 {
			return n
		}
		return 1
	}
}

// displayName возвращает отображаемое имя элемента: name либо имя файла.
func displayName(item config.VideoItem) string {
	if item.Name != "" {
		return item.Name
	}
	return filepath.Base(item.Path)
}

// formatDuration форматирует длительность обработки в читаемом виде.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.2fс", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// adjustExtension меняет расширение пути на ext, если оно не совпадает.
func adjustExtension(path, ext string) string {
	if strings.EqualFold(filepath.Ext(path), ext) {
		return path
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}
