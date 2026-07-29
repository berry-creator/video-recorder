//go:build !devcontainer && !headless && (windows || darwin || (linux && desktop))

package tray

import (
	"github.com/getlantern/systray"
)

type DesktopTray struct {
	onConfig     func()
	onOpenFolder func()
	onExit       func()
}

func NewTrayManager() TrayManager {
	return &DesktopTray{}
}

func (t *DesktopTray) Init(onConfig func(), onOpenFolder func(), onExit func()) {
	t.onConfig = onConfig
	t.onOpenFolder = onOpenFolder
	t.onExit = onExit
}

func (t *DesktopTray) Run() {
	systray.Run(t.onReady, t.onExitHandler)
}

func (t *DesktopTray) Quit() {
	systray.Quit()
}

func (t *DesktopTray) onReady() {
	templateIcon, regularIcon := trayIcons()
	systray.SetTemplateIcon(templateIcon, regularIcon)
	systray.SetTitle("")
	labels := labelsForLocale(systemLocale())
	systray.SetTooltip(labels.tooltip)

	mOpenFolder := systray.AddMenuItem("📂 "+labels.openFolder, labels.openFolderTip)
	mConfig := systray.AddMenuItem("⚙️ "+labels.configuration, labels.configurationTip)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("🚪 "+labels.quit, labels.quitTip)

	go func() {
		for {
			select {
			case <-mOpenFolder.ClickedCh:
				if t.onOpenFolder != nil {
					t.onOpenFolder()
				}
			case <-mConfig.ClickedCh:
				if t.onConfig != nil {
					t.onConfig()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (t *DesktopTray) onExitHandler() {
	if t.onExit != nil {
		t.onExit()
	}
}
