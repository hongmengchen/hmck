//go:build windows

package main

import "errors"

// paneSupported：Windows 上还没接 ConPTY，只能整屏打开。
// 提示栏据此不摆 p 键——摆出来的键按下去得有反应。
const paneSupported = false

func openChild(path string, cols, rows int) (ptyChild, error) {
	return nil, errors.New("Windows 上暂时只能全屏打开")
}
