package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"video-recorder/internal/config"
	"video-recorder/internal/service"
)

type testServer struct {
	server    *httptest.Server
	api       *Server
	exporter  *service.Exporter
	cancel    context.CancelFunc
	capture   *service.CaptureService
	hub       *service.FrameHub
	buffer    *service.CaptureBuffer
	recording *service.RecordingSession
}

type fakeCameraLister struct {
	devices      []service.CameraDevice
	capabilities service.CameraCapabilities
	err          error
}

type fakeDirectorySelector struct {
	directory string
	err       error
}

func (f fakeCameraLister) List(context.Context, string) ([]service.CameraDevice, error) {
	return f.devices, f.err
}

func (f fakeCameraLister) Capabilities(context.Context, string, string, string, string) (service.CameraCapabilities, error) {
	return f.capabilities, f.err
}

func (f fakeDirectorySelector) Select(context.Context, string) (string, error) {
	return f.directory, f.err
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	store, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := store.Get()
	cfg.Capture.FFmpegPath = "/path/that/does/not/exist"
	cfg.Storage.Directory = t.TempDir()
	if err := store.Update(cfg); err != nil {
		t.Fatal(err)
	}
	buffer, err := service.NewCaptureBuffer(time.Duration(cfg.Capture.BufferSeconds) * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recording, err := service.NewRecordingSession(buffer, time.Duration(cfg.Recording.MaxDurationMinutes)*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	hub := service.NewFrameHub()
	capture := service.NewCaptureService(recording, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	if err := capture.Start(ctx, cfg.Capture); err != nil {
		t.Fatal(err)
	}
	exporter := service.NewExporter(buffer, store.Get, logger)
	apiServer := NewServer(store, capture, recording, buffer, hub, exporter, logger)
	apiServer.cameras = fakeCameraLister{devices: []service.CameraDevice{}}
	apiServer.directory = fakeDirectorySelector{directory: cfg.Storage.Directory}
	handler := apiServer.Handler()
	return &testServer{server: httptest.NewServer(handler), api: apiServer, exporter: exporter, cancel: cancel, capture: capture, hub: hub, buffer: buffer, recording: recording}
}

func (s *testServer) close() {
	s.server.Close()
	s.cancel()
	s.capture.Stop()
	s.recording.Close()
	s.exporter.Close()
	_ = s.buffer.Close()
}

func TestConsolePageAndStatus(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Get(ts.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/console" {
		t.Fatalf("root response = %d Location %q, want 302 /console", response.StatusCode, response.Header.Get("Location"))
	}

	response, err = http.Get(ts.server.URL + "/console")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	pageText := string(page)
	if response.StatusCode != http.StatusOK || !strings.Contains(pageText, "录像控制台") {
		t.Fatalf("console page status = %d, body = %q", response.StatusCode, page)
	}
	for _, expected := range []string{"Recording Console", "navigator.languages", "videoRecorderLanguage", "新的录制", "New recording", "设备不可用", "Device unavailable", "重连中", "Reconnecting", "超时停止录制", `id="maxRecordingMinutes"`, `value="auto"`, `value="zh"`, `value="en"`, `id="device"`, `id="cameraMode"`, `id="pixelFormat"`, `id="videoCodec"`, `id="bufferSeconds"`, `id="serverPort"`, `id="storageOrganization"`, `id="resetConfigButton"`, "/api/v1/cameras", "/api/v1/cameras/capabilities", "/api/v1/storage/directory/select", "/api/v1/config/reset", "/api/v1/capture/reset"} {
		if !strings.Contains(pageText, expected) {
			t.Errorf("console page does not contain bilingual UI marker %q", expected)
		}
	}
	for _, order := range [][2]string{
		{`class="brand-name">Video Recorder`, `data-i18n="consoleTitle">录像控制台`},
		{`id="refreshButton"`, `id="languageSelect"`},
		{`id="exportButton"`, `id="resetCaptureButton"`},
	} {
		first, second := strings.Index(pageText, order[0]), strings.Index(pageText, order[1])
		if first < 0 || second < 0 || first >= second {
			t.Errorf("console elements are not ordered as %q before %q", order[0], order[1])
		}
	}
	response, err = http.Get(ts.server.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy /config status = %d, want 404", response.StatusCode)
	}

	response, err = http.Get(ts.server.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != float64(http.StatusOK) {
		t.Fatalf("status response = %#v", body)
	}
}

func TestResetConfigRestoresCaptureDefaultsWithoutClearingBuffer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()

	next := ts.api.config.Get()
	next.Capture.FPS = 12
	next.Capture.Width = 640
	next.Capture.Height = 480
	next.Capture.JPEGQuality = 12
	next.Capture.BufferSeconds = 15
	next.Capture.Source = "camera"
	next.Capture.Device = "/dev/video-test"
	next.Capture.PixelFormat = "nv12"
	next.Capture.VideoCodec = ""
	next.Capture.FFmpegPath = "/custom/ffmpeg"
	next.Server.Address = "127.0.0.1:9123"
	next.Server.AllowedOrigins = []string{"https://app.example.com"}
	next.Storage.Organization = config.StorageOrganizationMonth
	next.Export.QueueSize = 3
	if err := ts.api.config.Update(next); err != nil {
		t.Fatal(err)
	}
	if err := ts.buffer.Append(service.Frame{CapturedAt: time.Now(), Data: []byte("frame")}); err != nil {
		t.Fatal(err)
	}

	response, err := http.Post(ts.server.URL+"/api/v1/config/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Data config.Config `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	frames := ts.buffer.Stats().Frames
	defaults := config.Default()
	if response.StatusCode != http.StatusOK || body.Data.Capture.FPS != defaults.Capture.FPS || body.Data.Capture.Width != defaults.Capture.Width || body.Data.Capture.Height != defaults.Capture.Height || body.Data.Capture.JPEGQuality != defaults.Capture.JPEGQuality || body.Data.Capture.BufferSeconds != defaults.Capture.BufferSeconds {
		t.Fatalf("reset status = %d, config = %#v", response.StatusCode, body.Data)
	}
	if body.Data.Storage.Organization != config.StorageOrganizationMonth {
		t.Fatalf("reset changed storage organization to %q, want month", body.Data.Storage.Organization)
	}
	if body.Data.Capture.Source != next.Capture.Source || body.Data.Capture.Device != next.Capture.Device || body.Data.Capture.PixelFormat != next.Capture.PixelFormat || body.Data.Capture.VideoCodec != next.Capture.VideoCodec || body.Data.Capture.FFmpegPath != next.Capture.FFmpegPath || body.Data.Server.Address != next.Server.Address || len(body.Data.Server.AllowedOrigins) != 1 || body.Data.Storage.Directory != next.Storage.Directory || body.Data.Export.QueueSize != next.Export.QueueSize {
		t.Fatalf("reset changed a non-resettable setting: got %#v, before %#v", body.Data, next)
	}
	if frames != 1 {
		t.Fatalf("reset configuration cleared %d buffered frames, want 1 retained", frames)
	}
}

func TestMemoryBufferDurationDoesNotRequireCaptureRestart(t *testing.T) {
	previous := config.Default().Capture
	next := previous
	next.BufferSeconds++
	if captureRequiresRestart(previous, next) {
		t.Fatal("memory buffer duration change requires a capture restart")
	}
	next.FPS++
	if !captureRequiresRestart(previous, next) {
		t.Fatal("frame rate change did not require a capture restart")
	}
}

func TestApplicationStatePrioritizesDeviceAvailability(t *testing.T) {
	tests := []struct {
		name      string
		capture   service.CaptureStatus
		recording service.RecordingStatus
		want      string
	}{
		{name: "connecting", capture: service.CaptureStatus{Connecting: true}, recording: service.RecordingStatus{State: service.RecordingActive}, want: "reconnecting"},
		{name: "failed", capture: service.CaptureStatus{LastError: "camera missing"}, recording: service.RecordingStatus{State: service.RecordingActive}, want: "deviceUnavailable"},
		{name: "starting", capture: service.CaptureStatus{}, recording: service.RecordingStatus{State: service.RecordingPreviewing}, want: "reconnecting"},
		{name: "preview", capture: service.CaptureStatus{Running: true}, recording: service.RecordingStatus{State: service.RecordingPreviewing}, want: "previewing"},
		{name: "recording", capture: service.CaptureStatus{Running: true}, recording: service.RecordingStatus{State: service.RecordingActive}, want: "recording"},
		{name: "timed out", capture: service.CaptureStatus{Running: true}, recording: service.RecordingStatus{State: service.RecordingTimedOut}, want: "timedOut"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := applicationState(test.capture, test.recording); got != test.want {
				t.Fatalf("applicationState() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResetCaptureStartsRecordingAndClearsBufferedFrames(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	if err := ts.buffer.Append(service.Frame{CapturedAt: time.Now(), Data: []byte("frame")}); err != nil {
		t.Fatal(err)
	}

	response, err := http.Post(ts.server.URL+"/api/v1/capture/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	stats := ts.buffer.Stats()
	if response.StatusCode != http.StatusOK || stats.Frames != 0 || stats.Bytes != 0 || ts.recording.Status().State != service.RecordingActive {
		t.Fatalf("reset status = %d, segment stats = %#v", response.StatusCode, stats)
	}
}

func TestStatusReportsOnlyActiveRecordingDuration(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	assertStatusDuration(t, ts.server.URL, 0)
	if err := ts.recording.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	assertStatusDuration(t, ts.server.URL, 10)
}

func assertStatusDuration(t *testing.T, serverURL string, minimum int64) {
	t.Helper()
	response, err := http.Get(serverURL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Data struct {
			Buffer struct {
				DurationMillis int64 `json:"durationMillis"`
			} `json:"buffer"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Buffer.DurationMillis < minimum {
		t.Fatalf("recorded duration = %dms, want at least %dms", body.Data.Buffer.DurationMillis, minimum)
	}
}

func TestSelectStorageDirectory(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	ts.api.directory = fakeDirectorySelector{directory: filepath.Join(t.TempDir(), "Videos")}

	response, err := http.Post(ts.server.URL+"/api/v1/storage/directory/select", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Code int `json:"code"`
		Data struct {
			Directory string `json:"directory"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.Code != http.StatusOK || body.Data.Directory == "" {
		t.Fatalf("directory response status = %d, body = %#v", response.StatusCode, body)
	}
}

func TestCancelStorageDirectorySelection(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	ts.api.directory = fakeDirectorySelector{err: service.ErrDirectorySelectionCanceled}

	response, err := http.Post(ts.server.URL+"/api/v1/storage/directory/select", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("canceled directory selection status = %d, want 200", response.StatusCode)
	}
}

func TestCameraDevices(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	ts.api.cameras = fakeCameraLister{devices: []service.CameraDevice{
		{ID: "0", Name: "Integrated Camera"},
		{ID: "1", Name: "External Camera"},
	}}

	response, err := http.Get(ts.server.URL + "/api/v1/cameras")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Code int                    `json:"code"`
		Data []service.CameraDevice `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.Code != http.StatusOK || len(body.Data) != 2 {
		t.Fatalf("camera response status = %d, body = %#v", response.StatusCode, body)
	}
	if body.Data[0].ID != "0" || body.Data[0].Name != "Integrated Camera" {
		t.Fatalf("first camera = %#v", body.Data[0])
	}
}

func TestCameraCapabilities(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	ts.api.cameras = fakeCameraLister{capabilities: service.CameraCapabilities{
		Device:            "0",
		PixelFormats:      []string{"nv12", "uyvy422"},
		VideoCodecs:       []string{"mjpeg"},
		Modes:             []service.CameraMode{{VideoCodec: "mjpeg", Width: 1280, Height: 720, FPS: 30}},
		Recommended:       service.CameraMode{VideoCodec: "mjpeg", Width: 1280, Height: 720, FPS: 30},
		DrawtextAvailable: false,
	}}

	response, err := http.Get(ts.server.URL + "/api/v1/cameras/capabilities?device=0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Data service.CameraCapabilities `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.Data.Recommended.VideoCodec != "mjpeg" || body.Data.Recommended.FPS != 30 {
		t.Fatalf("capabilities status = %d, body = %#v", response.StatusCode, body)
	}
}

func TestSaveRejectsEmptyBuffer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	response, err := http.Post(ts.server.URL+"/api/v1/record/save?fileName=test", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("save status = %d, want 409", response.StatusCode)
	}
}

func TestSaveQueuesCurrentRecordingAndReturnsToPreviewWithoutRestartingCapture(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	if err := ts.recording.Start(); err != nil {
		t.Fatal(err)
	}
	if err := ts.recording.Record(service.Frame{CapturedAt: time.Now(), Data: []byte("frame")}); err != nil {
		t.Fatal(err)
	}

	response, err := http.Post(ts.server.URL+"/api/v1/record/save?fileName=test", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Data struct {
			JobID string `json:"jobId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	stats := ts.buffer.Stats()
	if response.StatusCode != http.StatusOK || body.Data.JobID == "" {
		t.Fatalf("save status = %d, body = %#v", response.StatusCode, body)
	}
	if stats.Frames != 0 || stats.Bytes != 0 {
		t.Fatalf("saved frames remain in the next segment: %#v", stats)
	}
	if ts.recording.Status().State != service.RecordingPreviewing {
		t.Fatalf("recording state = %q, want previewing", ts.recording.Status().State)
	}
	job, ok := ts.exporter.Status(body.Data.JobID)
	if !ok || job.FrameCount != 1 {
		t.Fatalf("queued job = %#v, found = %v", job, ok)
	}

	second, err := http.Post(ts.server.URL+"/api/v1/record/save?fileName=test-again", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second save status = %d, want 409 without newly captured frames", second.StatusCode)
	}
}

func TestCrossOriginRequestsRequireAllowlist(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	request, err := http.NewRequest(http.MethodGet, ts.server.URL+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://untrusted.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", response.StatusCode)
	}
}

func TestLivePreviewSendsBinaryJPEG(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	wsURL := "ws" + strings.TrimPrefix(ts.server.URL, "http") + "/ws/live"
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	deadline := time.Now().Add(time.Second)
	for ts.hub.SubscriberCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	want := []byte{0xff, 0xd8, 0x01, 0xff, 0xd9}
	ts.hub.Publish(service.Frame{CapturedAt: time.Now(), Data: want})
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	messageType, got, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.BinaryMessage || string(got) != string(want) {
		t.Fatalf("websocket message = (%d, %v)", messageType, got)
	}
}
