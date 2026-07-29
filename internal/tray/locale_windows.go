//go:build windows

package tray

import (
	"os"

	"golang.org/x/sys/windows"
)

var getUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

func systemLocale() string {
	languageID, _, _ := getUserDefaultUILanguage.Call()
	const (
		primaryLanguageMask = 0x3ff
		primaryChinese      = 0x04
	)
	if languageID != 0 {
		if languageID&primaryLanguageMask == primaryChinese {
			return "zh"
		}
		return "en"
	}
	return localeFromEnvironment(os.Getenv)
}
