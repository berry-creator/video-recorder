package service

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestMJPEGReaderExtractsConsecutiveFrames(t *testing.T) {
	first := []byte{0xff, 0xd8, 0x01, 0xff, 0xd9}
	second := []byte{0xff, 0xd8, 0x02, 0x03, 0xff, 0xd9}
	stream := append([]byte("noise"), first...)
	stream = append(stream, second...)
	reader := NewMJPEGReader(bytes.NewReader(stream))

	gotFirst, err := reader.ReadFrame()
	if err != nil || !bytes.Equal(gotFirst, first) {
		t.Fatalf("first ReadFrame() = %v, %v", gotFirst, err)
	}
	gotSecond, err := reader.ReadFrame()
	if err != nil || !bytes.Equal(gotSecond, second) {
		t.Fatalf("second ReadFrame() = %v, %v", gotSecond, err)
	}
	if _, err := reader.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("final ReadFrame() error = %v, want EOF", err)
	}
}

func TestMJPEGReaderRejectsTruncatedFrame(t *testing.T) {
	reader := NewMJPEGReader(bytes.NewReader([]byte{0xff, 0xd8, 0x01}))
	if _, err := reader.ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadFrame() error = %v, want unexpected EOF", err)
	}
}
