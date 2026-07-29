package service

import "time"

type Frame struct {
	CapturedAt time.Time
	Data       []byte
}
