package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"sync"
)

var (
	iconsOnce       sync.Once
	templateIconPNG []byte
	windowsIconICO  []byte
)

func trayIcons() ([]byte, []byte) {
	iconsOnce.Do(func() {
		templateIconPNG = encodePNG(drawTrayIcon(true))
		windowsPNG := encodePNG(drawTrayIcon(false))
		windowsIconICO = encodeICO(windowsPNG, 32, 32)
	})
	return templateIconPNG, windowsIconICO
}

func drawTrayIcon(template bool) *image.NRGBA {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	body := color.NRGBA{R: 30, G: 136, B: 229, A: 255}
	lens := color.NRGBA{R: 15, G: 76, B: 129, A: 255}
	record := color.NRGBA{R: 244, G: 67, B: 54, A: 255}
	if template {
		body = color.NRGBA{A: 255}
		lens = body
		record = color.NRGBA{}
	}

	// A compact camera silhouette remains recognizable after the menu bar
	// scales the source image down to 16 points.
	fillRoundedRect(img, 3, 7, 22, 25, 3, body)
	for x := 22; x <= 29; x++ {
		inset := (x - 22) / 2
		for y := 10 + inset; y <= 22-inset; y++ {
			img.SetNRGBA(x, y, lens)
		}
	}
	fillCircle(img, 12, 16, 4, record)

	return img
}

func fillRoundedRect(img *image.NRGBA, left, top, right, bottom, radius int, c color.NRGBA) {
	for y := top; y <= bottom; y++ {
		for x := left; x <= right; x++ {
			dx := max(left+radius-x, x-(right-radius), 0)
			dy := max(top+radius-y, y-(bottom-radius), 0)
			if dx*dx+dy*dy <= radius*radius {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func fillCircle(img *image.NRGBA, centerX, centerY, radius int, c color.NRGBA) {
	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			if dx, dy := x-centerX, y-centerY; dx*dx+dy*dy <= radius*radius {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func encodePNG(img image.Image) []byte {
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		panic(err)
	}
	return output.Bytes()
}

func encodeICO(pngData []byte, width, height byte) []byte {
	const directorySize = 6 + 16
	output := make([]byte, directorySize, directorySize+len(pngData))
	binary.LittleEndian.PutUint16(output[2:4], 1)
	binary.LittleEndian.PutUint16(output[4:6], 1)
	output[6] = width
	output[7] = height
	binary.LittleEndian.PutUint16(output[10:12], 1)
	binary.LittleEndian.PutUint16(output[12:14], 32)
	binary.LittleEndian.PutUint32(output[14:18], uint32(len(pngData)))
	binary.LittleEndian.PutUint32(output[18:22], directorySize)
	return append(output, pngData...)
}
