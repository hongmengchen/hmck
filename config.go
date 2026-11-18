package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// appConfig 是 hmck 自己的开关，和工具列表分开放。
//
// AutoDownload：启动检查完就把可更新的版本下载并校验到缓存，但不替换。
// AutoInstall：检查完直接下载并安装（隐含 AutoDownload）。
type appConfig struct {
	AutoDownload bool `json:"auto_download"`
	AutoInstall  bool `json:"auto_install"`
}

func configPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hmck", "config.json"), nil
}

func loadConfig() (appConfig, error) {
	var cfg appConfig
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("读不了 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return appConfig{}, fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	if cfg.AutoInstall {
		cfg.AutoDownload = true
	}
	return cfg, nil
}

func saveConfig(cfg appConfig) (string, error) {
	if cfg.AutoInstall {
		cfg.AutoDownload = true
	}
	path, err := configPath()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		if werr == nil {
			werr = cerr
		}
		return "", werr
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return "", err
	}
	return path, nil
}
