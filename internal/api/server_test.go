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
	server   *httptest.Server
	api      *Server
	exporter *service.Exporter
	cancel   context.CancelFunc
	capture  *service.CaptureService
	hub      *service.FrameHub
}

type fakeCameraLister struct {
	devices []service.CameraDevice
	err     error
}

type fakeDirectorySelector struct {
	directory string
	err       error
}

func (f fakeCameraLister) List(context.Context, string) ([]service.CameraDevice, error) {
	return f.devices, f.err
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
	ring := service.NewRingBuffer(time.Second)
	hub := service.NewFrameHub()
	capture := service.NewCaptureService(ring, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	if err := capture.Start(ctx, cfg.Capture); err != nil {
		t.Fatal(err)
	}
	exporter := service.NewExporter(ring, store.Get, logger)
	apiServer := NewServer(store, capture, ring, hub, exporter, logger)
	apiServer.cameras = fakeCameraLister{devices: []service.CameraDevice{}}
	apiServer.directory = fakeDirectorySelector{directory: cfg.Storage.Directory}
	handler := apiServer.Handler()
	return &testServer{server: httptest.NewServer(handler), api: apiServer, exporter: exporter, cancel: cancel, capture: capture, hub: hub}
}

func (s *testServer) close() {
	s.server.Close()
	s.cancel()
	s.capture.Stop()
	s.exporter.Close()
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
	if response.StatusCode != http.StatusOK || !strings.Contains(pageText, "采集控制台") {
		t.Fatalf("console page status = %d, body = %q", response.StatusCode, page)
	}
	for _, expected := range []string{"Capture Console", "navigator.languages", "videoRecorderLanguage", "保存并重新采集", "Save and start over", `value="auto"`, `value="zh"`, `value="en"`, `id="device"`, `id="serverPort"`, `id="storageOrganization"`, `id="resetConfigButton"`, "/api/v1/cameras", "/api/v1/storage/directory/select", "/api/v1/config/reset", "/api/v1/capture/reset"} {
		if !strings.Contains(pageText, expected) {
			t.Errorf("console page does not contain bilingual UI marker %q", expected)
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
	next.Capture.BufferSeconds = 15
	next.Capture.JPEGQuality = 12
	next.Capture.Source = "camera"
	next.Capture.Device = "/dev/video-test"
	next.Capture.FFmpegPath = "/custom/ffmpeg"
	next.Server.Address = "127.0.0.1:9123"
	next.Server.AllowedOrigins = []string{"https://app.example.com"}
	next.Storage.Organization = config.StorageOrganizationMonth
	next.Export.QueueSize = 3
	if err := ts.api.config.Update(next); err != nil {
		t.Fatal(err)
	}
	ts.api.ring.Append(service.Frame{CapturedAt: time.Now(), Data: []byte("frame")})

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
	frames, _, _, _ := ts.api.ring.Stats()
	defaults := config.Default()
	if response.StatusCode != http.StatusOK || body.Data.Capture.FPS != defaults.Capture.FPS || body.Data.Capture.Width != defaults.Capture.Width || body.Data.Capture.Height != defaults.Capture.Height || body.Data.Capture.BufferSeconds != defaults.Capture.BufferSeconds || body.Data.Capture.JPEGQuality != defaults.Capture.JPEGQuality {
		t.Fatalf("reset status = %d, config = %#v", response.StatusCode, body.Data)
	}
	if body.Data.Storage.Organization != config.StorageOrganizationMonth {
		t.Fatalf("reset changed storage organization to %q, want month", body.Data.Storage.Organization)
	}
	if body.Data.Capture.Source != next.Capture.Source || body.Data.Capture.Device != next.Capture.Device || body.Data.Capture.FFmpegPath != next.Capture.FFmpegPath || body.Data.Server.Address != next.Server.Address || len(body.Data.Server.AllowedOrigins) != 1 || body.Data.Storage.Directory != next.Storage.Directory || body.Data.Export.QueueSize != next.Export.QueueSize {
		t.Fatalf("reset changed a non-resettable setting: got %#v, before %#v", body.Data, next)
	}
	if frames != 1 {
		t.Fatalf("reset configuration cleared %d buffered frames, want 1 retained", frames)
	}
}

func TestResetCaptureClearsBufferedFrames(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	ts.api.ring.Append(service.Frame{CapturedAt: time.Now(), Data: []byte("frame")})

	response, err := http.Post(ts.server.URL+"/api/v1/capture/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	frames, size, _, _ := ts.api.ring.Stats()
	if response.StatusCode != http.StatusOK || frames != 0 || size != 0 {
		t.Fatalf("reset status = %d, ring stats = (%d, %d)", response.StatusCode, frames, size)
	}
}

func TestStatusReportsTimeSinceCaptureSessionStarted(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	time.Sleep(20 * time.Millisecond)

	response, err := http.Get(ts.server.URL + "/api/v1/status")
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
	if body.Data.Buffer.DurationMillis < 10 {
		t.Fatalf("captured duration = %dms, want time elapsed since the session started", body.Data.Buffer.DurationMillis)
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

func TestSaveQueuesCurrentFramesAndStartsNewCapture(t *testing.T) {
	ts := newTestServer(t)
	defer ts.close()
	previousStart := ts.capture.Status().StartedAt
	time.Sleep(time.Millisecond)
	ts.api.ring.Append(service.Frame{CapturedAt: time.Now(), Data: []byte("frame")})

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
	frames, size, _, _ := ts.api.ring.Stats()
	currentStart := ts.capture.Status().StartedAt
	if response.StatusCode != http.StatusOK || body.Data.JobID == "" {
		t.Fatalf("save status = %d, body = %#v", response.StatusCode, body)
	}
	if frames != 0 || size != 0 {
		t.Fatalf("saved frames remain in the next capture: frames=%d size=%d", frames, size)
	}
	if !currentStart.After(previousStart) {
		t.Fatalf("capture start = %v, want after %v", currentStart, previousStart)
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
