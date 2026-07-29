package service

import "sync"

type FrameHub struct {
	mu          sync.RWMutex
	subscribers map[chan Frame]struct{}
}

func NewFrameHub() *FrameHub {
	return &FrameHub{subscribers: make(map[chan Frame]struct{})}
}

func (h *FrameHub) Subscribe() (<-chan Frame, func()) {
	ch := make(chan Frame, 1)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
}

func (h *FrameHub) Publish(frame Frame) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers {
		select {
		case ch <- frame:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- frame:
			default:
			}
		}
	}
}

func (h *FrameHub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
