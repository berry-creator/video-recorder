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
