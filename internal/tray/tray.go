package tray

type TrayManager interface {
	Init(onConfigClick func(), onOpenFolderClick func(), onExit func())
	Run()
	Quit()
}
