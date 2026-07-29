package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParseAVFoundationDevices(t *testing.T) {
	output := `[AVFoundation indev @ 0x1] AVFoundation video devices:
[AVFoundation indev @ 0x1] [0] FaceTime HD Camera
[AVFoundation indev @ 0x1] [1] External Camera
[AVFoundation indev @ 0x1] [2] Capture screen 0
[AVFoundation indev @ 0x1] AVFoundation audio devices:
[AVFoundation indev @ 0x1] [0] MacBook Microphone`
	want := []CameraDevice{{ID: "0", Name: "FaceTime HD Camera"}, {ID: "1", Name: "External Camera"}}
	if got := parseAVFoundationDevices(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAVFoundationDevices() = %#v, want %#v", got, want)
	}
}

func TestParseDirectShowDevicesUsesStableAlternativeName(t *testing.T) {
	output := `[dshow @ 000001] "Integrated Camera" (video)
[dshow @ 000001]   Alternative name "@device_pnp_\\?\usb#vid_0001"
[dshow @ 000001] "External Camera" (video)
[dshow @ 000001]   Alternative name "@device_pnp_\\?\usb#vid_0002"
[dshow @ 000001] "Microphone" (audio)`
	want := []CameraDevice{
		{ID: `@device_pnp_\\?\usb#vid_0001`, Name: "Integrated Camera"},
		{ID: `@device_pnp_\\?\usb#vid_0002`, Name: "External Camera"},
	}
	if got := parseDirectShowDevices(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDirectShowDevices() = %#v, want %#v", got, want)
	}
}

func TestCameraDetectorListsLinuxDevices(t *testing.T) {
	detector := &CameraDetector{
		platform: "linux",
		glob: func(string) ([]string, error) {
			return []string{"/dev/video1", "/dev/video0"}, nil
		},
		readFile: func(path string) ([]byte, error) {
			if path == "/sys/class/video4linux/video0/name" {
				return []byte("Integrated Camera\n"), nil
			}
			return nil, errors.New("not found")
		},
	}
	want := []CameraDevice{{ID: "/dev/video0", Name: "Integrated Camera"}, {ID: "/dev/video1", Name: "video1"}}
	got, err := detector.List(context.Background(), "unused")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestCameraDetectorReportsFFmpegFailure(t *testing.T) {
	detector := &CameraDetector{
		platform: "darwin",
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Unknown input format: avfoundation"), errors.New("exit status 1")
		},
	}
	if _, err := detector.List(context.Background(), "ffmpeg"); err == nil {
		t.Fatal("List() accepted FFmpeg output that was not a camera device list")
	}
}

func TestParseAVFoundationCapabilities(t *testing.T) {
	formatsOutput := `[in#0 @ 0x1] Supported pixel formats:
[in#0 @ 0x1]   uyvy422
[in#0 @ 0x1]   yuyv422
[in#0 @ 0x1]   nv12
[AVFilterGraph @ 0x2] unrelated error`
	formats := parseAVFoundationPixelFormats(formatsOutput)
	wantFormats := []string{"uyvy422", "yuyv422", "nv12"}
	if !reflect.DeepEqual(formats, wantFormats) {
		t.Fatalf("pixel formats = %#v, want %#v", formats, wantFormats)
	}
	modesOutput := `[avfoundation @ 0x1] Supported modes:
[avfoundation @ 0x1]   1920x1080@[1.000000 30.000000]fps
[avfoundation @ 0x1]   1280x720@[1.000000 30.000000]fps`
	modes := parseAVFoundationModes(modesOutput, "nv12")
	wantModes := []CameraMode{
		{PixelFormat: "nv12", Width: 1920, Height: 1080, FPS: 30},
		{PixelFormat: "nv12", Width: 1280, Height: 720, FPS: 30},
	}
	if !reflect.DeepEqual(modes, wantModes) {
		t.Fatalf("modes = %#v, want %#v", modes, wantModes)
	}
	if recommended := recommendCameraMode("nv12", "", formats, nil, modes); recommended != wantModes[1] {
		t.Fatalf("recommended mode = %#v, want %#v", recommended, wantModes[1])
	}
}

func TestParseDirectShowCapabilities(t *testing.T) {
	output := `[dshow @ 0001]   pixel_format=yuyv422 min s=640x480 fps=5 max s=1280x720 fps=30
[dshow @ 0001]   pixel_format=nv12 min s=1280x720 fps=5 max s=1920x1080 fps=30
[dshow @ 0001]   vcodec=mjpeg min s=1280x720 fps=5 max s=1280x720 fps=30`
	formats, codecs, modes := parseDirectShowCapabilities(output)
	if !reflect.DeepEqual(formats, []string{"yuyv422", "nv12"}) {
		t.Fatalf("pixel formats = %#v", formats)
	}
	if !reflect.DeepEqual(codecs, []string{"mjpeg"}) {
		t.Fatalf("video codecs = %#v", codecs)
	}
	if len(modes) != 10 || modes[3] != (CameraMode{PixelFormat: "yuyv422", Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("modes = %#v", modes)
	}
	if recommended := recommendCameraMode("", "", formats, codecs, modes); recommended != (CameraMode{VideoCodec: "mjpeg", Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("recommended mode = %#v", recommended)
	}
}

func TestParseV4L2Capabilities(t *testing.T) {
	output := `[video4linux2,v4l2 @ 0x1] Raw       : yuyv422 : YUYV 4:2:2 : 640x480 1280x720
[video4linux2,v4l2 @ 0x1] Compressed: mjpeg : Motion-JPEG : 1280x720`
	formats, modes := parseV4L2Capabilities(output)
	if !reflect.DeepEqual(formats, []string{"yuyv422"}) || len(modes) != 2 {
		t.Fatalf("formats = %#v, modes = %#v", formats, modes)
	}
}

func TestHasFFmpegFilter(t *testing.T) {
	output := " T.C drawbox V->V Draw a colored box\n T.S. drawtext V->V Draw text"
	if !hasFFmpegFilter(output, "drawtext") || hasFFmpegFilter(output, "overlay") {
		t.Fatalf("unexpected filter detection for %q", output)
	}
}

func TestAVFoundationCapabilitiesProbeSelectedDevice(t *testing.T) {
	var calls [][]string
	detector := &CameraDetector{
		platform: "darwin",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			switch {
			case containsString(args, "-filters"):
				return []byte(" T.S. drawtext V->V Draw text"), nil
			case containsString(args, "monob"):
				return []byte("[in#0 @ 0x1] Supported pixel formats:\n[in#0 @ 0x1] nv12"), errors.New("unsupported pixel format")
			default:
				return []byte("[avfoundation @ 0x1] 1280x720@[1.000000 30.000000]fps"), errors.New("unsupported mode")
			}
		},
	}

	capabilities, err := detector.Capabilities(context.Background(), "ffmpeg", "7", "nv12", "")
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Device != "7" || capabilities.Recommended != (CameraMode{PixelFormat: "nv12", Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	for _, call := range calls[1:] {
		if input := argumentAfter(call, "-i"); input != "7:none" {
			t.Fatalf("AVFoundation probe input = %q, want selected device 7:none; args = %#v", input, call)
		}
	}
}
