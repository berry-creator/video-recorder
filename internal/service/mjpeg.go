package service

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const maxJPEGFrameSize = 32 << 20

type MJPEGReader struct {
	reader *bufio.Reader
}

func NewMJPEGReader(reader io.Reader) *MJPEGReader {
	return &MJPEGReader{reader: bufio.NewReaderSize(reader, 256*1024)}
}

func (r *MJPEGReader) ReadFrame() ([]byte, error) {
	started := false
	previous := byte(0)
	frame := make([]byte, 0, 128*1024)
	for {
		current, err := r.reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && started {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if !started {
			if previous == 0xff && current == 0xd8 {
				started = true
				frame = append(frame, 0xff, 0xd8)
			}
			previous = current
			continue
		}
		frame = append(frame, current)
		if len(frame) > maxJPEGFrameSize {
			return nil, fmt.Errorf("JPEG frame exceeds %d bytes", maxJPEGFrameSize)
		}
		if previous == 0xff && current == 0xd9 {
			return frame, nil
		}
		previous = current
	}
}
