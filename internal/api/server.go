package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"video-recorder/internal/config"
	"video-recorder/internal/service"
	webui "video-recorder/web"
)

type Server struct {
	config    *config.Store
	capture   *service.CaptureService
	recording *service.RecordingSession
	buffer    *service.CaptureBuffer
	hub       *service.FrameHub
	exporter  *service.Exporter
	log       *slog.Logger
	cameras   cameraLister
	directory directorySelector
	upgrader  websocket.Upgrader
}

type cameraLister interface {
	List(context.Context, string) ([]service.CameraDevice, error)
	Capabilities(context.Context, string, string, string) (service.CameraCapabilities, error)
}

type directorySelector interface {
	Select(context.Context, string) (string, error)
}

func NewServer(store *config.Store, capture *service.CaptureService, recording *service.RecordingSession, buffer *service.CaptureBuffer, hub *service.FrameHub, exporter *service.Exporter, logger *slog.Logger) *Server {
	server := &Server{
		config:    store,
		capture:   capture,
		recording: recording,
		buffer:    buffer,
		hub:       hub,
		exporter:  exporter,
		log:       logger,
		cameras:   service.NewCameraDetector(),
		directory: service.NewDirectoryPicker(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 256 * 1024,
		},
	}
	server.upgrader.CheckOrigin = server.originAllowed
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console", http.StatusFound)
	})
	mux.HandleFunc("GET /console", s.consolePage)
	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PUT /api/v1/config", s.updateConfig)
	mux.HandleFunc("POST /api/v1/config/reset", s.resetConfig)
	mux.HandleFunc("GET /api/v1/cameras", s.getCameras)
	mux.HandleFunc("GET /api/v1/cameras/capabilities", s.getCameraCapabilities)
	mux.HandleFunc("POST /api/v1/storage/directory/select", s.selectStorageDirectory)
	mux.HandleFunc("GET /api/v1/status", s.getStatus)
	mux.HandleFunc("POST /api/v1/capture/reset", s.resetCapture)
	mux.HandleFunc("POST /api/v1/record/save", s.saveRecording)
	mux.HandleFunc("GET /api/v1/record/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /ws/live", s.livePreview)
	return s.middleware(mux)
}

func (s *Server) selectStorageDirectory(w http.ResponseWriter, r *http.Request) {
	directory, err := s.directory.Select(r.Context(), s.config.Get().Storage.Directory)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrDirectorySelectionCanceled):
			writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "directory selection canceled", Data: map[string]any{"canceled": true}})
		case errors.Is(err, service.ErrDirectorySelectionBusy):
			writeError(w, http.StatusConflict, err.Error())
		default:
			s.log.Warn("directory selection failed", "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "ok", Data: map[string]any{"directory": directory}})
}

func (s *Server) getCameras(w http.ResponseWriter, r *http.Request) {
	devices, err := s.cameras.List(r.Context(), s.config.Get().Capture.FFmpegPath)
	if err != nil {
		s.log.Warn("camera detection failed", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "ok", Data: devices})
}

func (s *Server) getCameraCapabilities(w http.ResponseWriter, r *http.Request) {
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	if device == "" {
		writeError(w, http.StatusBadRequest, "camera device is required")
		return
	}
	capabilities, err := s.cameras.Capabilities(
		r.Context(),
		s.config.Get().Capture.FFmpegPath,
		device,
		strings.TrimSpace(r.URL.Query().Get("pixelFormat")),
	)
	if err != nil {
		s.log.Warn("camera capability detection failed", "device", device, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "ok", Data: capabilities})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		origin := r.Header.Get("Origin")
		if !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, "origin is not allowed")
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		started := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := strings.TrimSuffix(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	for _, allowed := range s.config.Get().Server.AllowedOrigins {
		allowed = strings.TrimSuffix(strings.TrimSpace(allowed), "/")
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func (s *Server) consolePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(webui.IndexHTML)
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "ok", Data: s.config.Get()})
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var next config.Config
	if err := decoder.Decode(&next); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid configuration: %v", err))
		return
	}
	previous := s.config.Get()
	if err := s.config.Update(next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stored := s.config.Get()
	if previous.Capture.BufferSeconds != stored.Capture.BufferSeconds {
		if err := s.buffer.SetMemoryDuration(time.Duration(stored.Capture.BufferSeconds) * time.Second); err != nil {
			writeError(w, http.StatusInternalServerError, "configuration saved but memory buffer update failed: "+err.Error())
			return
		}
	}
	if previous.Recording.MaxDurationMinutes != stored.Recording.MaxDurationMinutes {
		if err := s.recording.SetMaxDuration(time.Duration(stored.Recording.MaxDurationMinutes) * time.Minute); err != nil {
			writeError(w, http.StatusInternalServerError, "configuration saved but recording timeout update failed: "+err.Error())
			return
		}
	}
	if captureRequiresRestart(previous.Capture, stored.Capture) {
		if err := s.capture.Reconfigure(stored.Capture); err != nil {
			writeError(w, http.StatusInternalServerError, "configuration saved but capture restart failed: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "configuration saved", Data: stored})
}

func (s *Server) resetConfig(w http.ResponseWriter, _ *http.Request) {
	next := s.config.Get()
	previousCapture := next.Capture
	defaults := config.Default()
	next.Capture.Width = defaults.Capture.Width
	next.Capture.Height = defaults.Capture.Height
	next.Capture.FPS = defaults.Capture.FPS
	next.Capture.JPEGQuality = defaults.Capture.JPEGQuality
	next.Capture.BufferSeconds = defaults.Capture.BufferSeconds
	if err := s.config.Update(next); err != nil {
		writeError(w, http.StatusInternalServerError, "reset configuration: "+err.Error())
		return
	}
	if previousCapture.BufferSeconds != next.Capture.BufferSeconds {
		if err := s.buffer.SetMemoryDuration(time.Duration(next.Capture.BufferSeconds) * time.Second); err != nil {
			writeError(w, http.StatusInternalServerError, "configuration reset but memory buffer update failed: "+err.Error())
			return
		}
	}
	if captureRequiresRestart(previousCapture, next.Capture) {
		if err := s.capture.Reconfigure(next.Capture); err != nil {
			writeError(w, http.StatusInternalServerError, "configuration reset but capture restart failed: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "configuration reset", Data: s.config.Get()})
}

func captureRequiresRestart(previous, next config.CaptureConfig) bool {
	previous.BufferSeconds = next.BufferSeconds
	return previous != next
}

func (s *Server) getStatus(w http.ResponseWriter, _ *http.Request) {
	stats := s.buffer.Stats()
	captureStatus := s.capture.Status()
	recordingStatus := s.recording.Status()
	capturedDuration := time.Duration(0)
	if recordingStatus.State == service.RecordingActive && !recordingStatus.StartedAt.IsZero() {
		capturedDuration = time.Since(recordingStatus.StartedAt)
		if capturedDuration < 0 {
			capturedDuration = 0
		}
	}
	state := applicationState(captureStatus, recordingStatus)
	data := map[string]any{
		"state":     state,
		"capture":   captureStatus,
		"recording": recordingStatus,
		"buffer": map[string]any{
			"frames":         stats.Frames,
			"bytes":          stats.Bytes,
			"memoryBytes":    stats.MemoryBytes,
			"diskBytes":      stats.DiskBytes,
			"oldest":         stats.Oldest,
			"newest":         stats.Newest,
			"durationMillis": capturedDuration.Milliseconds(),
		},
		"previewClients": s.hub.SubscriberCount(),
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "ok", Data: data})
}

func applicationState(capture service.CaptureStatus, recording service.RecordingStatus) string {
	if capture.Connecting {
		return "reconnecting"
	}
	if !capture.Running {
		if capture.LastError != "" {
			return "deviceUnavailable"
		}
		return "reconnecting"
	}
	return string(recording.State)
}

func (s *Server) resetCapture(w http.ResponseWriter, _ *http.Request) {
	if err := s.recording.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, "start new recording: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "new recording started", Data: s.recording.Status()})
}

func (s *Server) saveRecording(w http.ResponseWriter, r *http.Request) {
	status, err := s.recording.Save(s.exporter, r.URL.Query().Get("fileName"))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrRecordingNotActive):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, service.ErrNoFrames):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, service.ErrQueueFull):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	data := map[string]any{"fileName": status.FileName, "jobId": status.ID, "state": status.State}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "video export task accepted; live preview continues", Data: data})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	status, ok := s.exporter.Status(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "export job not found")
		return
	}
	writeJSON(w, http.StatusOK, response{Code: http.StatusOK, Message: "ok", Data: status})
}

func (s *Server) livePreview(w http.ResponseWriter, r *http.Request) {
	connection, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("websocket upgrade failed", "error", err)
		return
	}
	defer connection.Close()
	frames, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		connection.SetReadLimit(1024)
		_ = connection.SetReadDeadline(time.Now().Add(45 * time.Second))
		connection.SetPongHandler(func(string) error {
			return connection.SetReadDeadline(time.Now().Add(45 * time.Second))
		})
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-closed:
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}
			_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := connection.WriteMessage(websocket.BinaryMessage, frame.Data); err != nil {
				return
			}
		case <-ping.C:
			_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, response{Code: status, Message: strings.TrimSpace(message)})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
