package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"

	"open-zone/internal/proto"
)

const (
	defaultConfigName = "config"
)

type Config struct {
	DP8Port  int
	NewsPort int
	AutoPort int

	ServerCreatedBy string
	ServerVersion   string
	ServerTagline   string

	ShimPath string

	// DP8LogPath enables NDJSON telemetry when set. Leave empty to disable file logging.
	DP8LogPath string

	Proto proto.EngineConfig
}

func Load() (Config, error) {
	v := viper.New()
	v.SetConfigName(defaultConfigName)
	v.SetConfigType("yaml")

	// "Right" project structure: config lives under repo-root config/.
	// Also support running from other CWDs by searching upwards via explicit paths.
	v.AddConfigPath(".")
	v.AddConfigPath("config")

	v.SetEnvPrefix("OZ")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Defaults (match current known-good behavior).
	v.SetDefault("dp8.port", 2300)
	v.SetDefault("news.port", 2301)
	v.SetDefault("autoupdate.port", 80)
	v.SetDefault("shim.path", "bin\\dp8shim.dll")

	v.SetDefault("server.created_by", "")
	v.SetDefault("server.version", "0.1.0")
	v.SetDefault("server.tagline", "Open ZoneMatch server")

	v.SetDefault("telemetry.dp8_ndjson_path", "")

	// Config file is optional; env-only is fine.
	_ = v.ReadInConfig()

	cfg := Config{
		DP8Port:         v.GetInt("dp8.port"),
		NewsPort:        v.GetInt("news.port"),
		AutoPort:        v.GetInt("autoupdate.port"),
		ServerCreatedBy: strings.TrimSpace(v.GetString("server.created_by")),
		ServerVersion:   strings.TrimSpace(v.GetString("server.version")),
		ServerTagline:   strings.TrimSpace(v.GetString("server.tagline")),
		ShimPath:        v.GetString("shim.path"),
		DP8LogPath:      v.GetString("telemetry.dp8_ndjson_path"),
		Proto: proto.EngineConfig{
			Port: 0, // set below
		},
	}

	if cfg.DP8Port <= 0 || cfg.DP8Port > 65535 {
		return Config{}, fmt.Errorf("invalid dp8.port %d", cfg.DP8Port)
	}
	if cfg.NewsPort <= 0 || cfg.NewsPort > 65535 {
		return Config{}, fmt.Errorf("invalid news.port %d", cfg.NewsPort)
	}
	if cfg.AutoPort < 0 || cfg.AutoPort > 65535 {
		return Config{}, fmt.Errorf("invalid autoupdate.port %d", cfg.AutoPort)
	}
	if strings.TrimSpace(cfg.ShimPath) == "" {
		return Config{}, fmt.Errorf("shim.path must not be empty")
	}
	if cfg.ServerVersion == "" {
		return Config{}, fmt.Errorf("server.version must not be empty")
	}
	cfg.Proto.Port = cfg.DP8Port

	if strings.TrimSpace(cfg.DP8LogPath) != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.DP8LogPath), 0o755); err != nil {
			return Config{}, fmt.Errorf("create telemetry dir: %w", err)
		}
	}
	return cfg, nil
}
