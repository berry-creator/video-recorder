//go:build devcontainer || headless || (linux && !desktop)

package tray

import (
	"log"
	"sync"
)

type MockTray struct {
	done chan struct{}
	once sync.Once
}

func NewTrayManager() TrayManager {
	return &MockTray{done: make(chan struct{})}
}

func (m *MockTray) Init(onConfigClick func(), onOpenFolderClick func(), onExit func()) {
	log.Println("[HEADLESS] Mock Tray initialized. Desktop UI disabled.")
}

func (m *MockTray) Run() {
	log.Println("[HEADLESS] Mock Tray is running silently...")
	<-m.done
}

func (m *MockTray) Quit() {
	m.once.Do(func() { close(m.done) })
}
