//go:build !linux

package tui

import "os"

func beginQuickInput(*os.File) (func(), bool) { return nil, false }
