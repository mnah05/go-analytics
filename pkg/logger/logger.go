package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go-analytics/internal/config"

	"github.com/rs/zerolog"
)

var (
	global     zerolog.Logger
	globalOnce sync.Once
)

func NewLogger(cfg *config.Config, defaultFile string) (zerolog.Logger, func() error) {
	switch cfg.LogOutput {
	case "file", "both":
		filePath := cfg.LogFile
		if filePath == "" {
			filePath = defaultFile
		}
		logg, cleanup, err := NewWithFile(filePath, cfg.LogOutput == "both", cfg.LogLevel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create logger: %v\n", err)
			os.Exit(1)
		}
		return logg, cleanup
	default:
		return NewProduction(cfg.LogLevel), func() error { return nil }
	}
}

func ParseLevel(level string) zerolog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

func New() zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02 15:04:05",
	}).With().Timestamp().Logger()
}

func NewProduction(level string) zerolog.Logger {
	return zerolog.New(os.Stdout).
		With().Timestamp().Logger().
		Level(ParseLevel(level))
}

func NewWithFile(filePath string, console bool, level string) (zerolog.Logger, func() error, error) {
	var writers []io.Writer
	var file *os.File

	if console {
		writers = append(writers, zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "2006-01-02 15:04:05",
		})
	}

	if filePath != "" {
		dir := filepath.Dir(filePath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return zerolog.Logger{}, nil, fmt.Errorf("create log dir: %w", err)
			}
		}

		var err error
		file, err = os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return zerolog.Logger{}, nil, fmt.Errorf("open log file: %w", err)
		}
		writers = append(writers, file)
	}

	var output io.Writer
	if len(writers) == 1 {
		output = writers[0]
	} else {
		output = zerolog.MultiLevelWriter(writers...)
	}

	logger := zerolog.New(output).With().Timestamp().Logger().Level(ParseLevel(level))

	cleanup := func() error {
		if file != nil {
			return file.Close()
		}
		return nil
	}

	return logger, cleanup, nil
}

func Global() zerolog.Logger {
	globalOnce.Do(func() {
		global = New()
	})
	return global
}
