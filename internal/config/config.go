package config

import (
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
)

type Config struct {
	AppPort string `env:"APP_PORT" envDefault:"8080"`

	DatabaseURL string `env:"DATABASE_URL,required"`

	RedisAddr     string `env:"REDIS_ADDR,required"`
	RedisPassword string `env:"REDIS_PASSWORD"`
	RedisDB       int    `env:"REDIS_DB" envDefault:"0"`

	WorkerConcurrency int `env:"WORKER_CONCURRENCY" envDefault:"10"`

	HealthCheckTimeout    time.Duration `env:"HEALTH_CHECK_TIMEOUT" envDefault:"2s"`
	APIShutdownTimeout    time.Duration `env:"API_SHUTDOWN_TIMEOUT" envDefault:"10s"`
	WorkerShutdownTimeout time.Duration `env:"WORKER_SHUTDOWN_TIMEOUT" envDefault:"30s"`

	LogOutput string `env:"LOG_OUTPUT" envDefault:"stdout"`
	LogFile   string `env:"LOG_FILE"`
	LogLevel  string `env:"LOG_LEVEL" envDefault:"info"`
}

var (
	cfg  *Config
	once sync.Once
)

func Load(logg zerolog.Logger) *Config {
	once.Do(func() {
		if err := godotenv.Load(); err != nil {
			logg.Info().Msg("no .env file found (using system environment)")
		}

		c := Config{}

		if err := env.Parse(&c); err != nil {
			logg.Fatal().Err(err).Msg("failed to load config")
		}

		if c.DatabaseURL == "" {
			logg.Fatal().Msg("DATABASE_URL is required")
		}
		if err := validateDatabaseURL(c.DatabaseURL); err != nil {
			logg.Fatal().Err(err).Msg("invalid DATABASE_URL")
		}
		if c.RedisAddr == "" {
			logg.Fatal().Msg("REDIS_ADDR is required")
		}
		if c.RedisDB < 0 || c.RedisDB > 15 {
			logg.Fatal().Msg("REDIS_DB must be between 0 and 15")
		}
		if err := validatePort(c.AppPort); err != nil {
			logg.Fatal().Err(err).Msg("invalid APP_PORT")
		}
		if c.HealthCheckTimeout <= 0 {
			logg.Fatal().Msg("HEALTH_CHECK_TIMEOUT must be positive")
		}
		if c.APIShutdownTimeout <= 0 {
			logg.Fatal().Msg("API_SHUTDOWN_TIMEOUT must be positive")
		}
		if c.WorkerShutdownTimeout <= 0 {
			logg.Fatal().Msg("WORKER_SHUTDOWN_TIMEOUT must be positive")
		}
		if c.WorkerConcurrency <= 0 {
			logg.Fatal().Msg("WORKER_CONCURRENCY must be positive")
		}

		if c.LogOutput != "stdout" && c.LogOutput != "file" && c.LogOutput != "both" {
			logg.Fatal().Msg("LOG_OUTPUT must be one of: stdout, file, both")
		}
		if (c.LogOutput == "file" || c.LogOutput == "both") && c.LogFile == "" {
			logg.Fatal().Msg("LOG_FILE is required when LOG_OUTPUT is file or both")
		}

		cfg = &c
	})

	return cfg
}

func validatePort(port string) error {
	if port == "" {
		return fmt.Errorf("port cannot be empty")
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("port must be a number")
	}
	if p < 1 || p > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func validateDatabaseURL(dbURL string) error {
	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("URL scheme must be 'postgres' or 'postgresql', got '%s'", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL must contain a host")
	}
	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("URL must contain a database name")
	}
	return nil
}
