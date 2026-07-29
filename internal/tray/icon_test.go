package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestTrayIcons(t *testing.T) {
	templateIcon, windowsIcon := trayIcons()

	img, err := png.Decode(bytes.NewReader(templateIcon))
	if err != nil {
		t.Fatalf("decode template icon: %v", err)
	}
	if got := img.Bounds().Size(); got.X != 32 || got.Y != 32 {
		t.Fatalf("template icon size = %v, want 32x32", got)
	}

	if len(windowsIcon) < 22 || binary.LittleEndian.Uint16(windowsIcon[2:4]) != 1 {
		t.Fatal("Windows icon does not contain a valid ICO directory")
	}
	offset := binary.LittleEndian.Uint32(windowsIcon[18:22])
	if offset != 22 {
		t.Fatalf("Windows icon image offset = %d, want 22", offset)
	}
	if _, err := png.Decode(bytes.NewReader(windowsIcon[offset:])); err != nil {
		t.Fatalf("decode PNG stored in Windows icon: %v", err)
	}
}
