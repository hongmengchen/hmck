//go:build !windows

package main

// macOS / Linux 的 install.sh 一直用 ~/.local/bin（root 用 /usr/local/bin），没有旧目录要搬
func legacyInstallDirs() []string { return nil }

func isLegacyPath(string) bool { return false }

func cleanUserPath(string, []string) {}
