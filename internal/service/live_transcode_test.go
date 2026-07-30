package service

import (
	"reflect"
	"testing"

	"video-recorder/internal/config"
)

func TestEncoderCandidatesPreferHardwareAndCapSoftwareThreads(t *testing.T) {
	tests := []struct {
		platform string
		first    string
	}{
		{platform: "darwin", first: "h264_videotoolbox"},
		{platform: "windows", first: "h264_qsv"},
		{platform: "linux", first: "h264_vaapi"},
	}
	for _, test := range tests {
		candidates := encoderCandidates(test.platform, config.ExportEncoderAuto, 2, 1000)
		if candidates[0].name != test.first || candidates[len(candidates)-1].name != "libx264" {
			t.Fatalf("%s candidates = %#v", test.platform, candidates)
		}
		if args := candidates[len(candidates)-1].args; !reflect.DeepEqual(args, []string{"-c:v", "libx264", "-preset", "veryfast", "-b:v", "1000k", "-maxrate", "1100k", "-bufsize", "2000k", "-threads", "2", "-pix_fmt", "yuv420p"}) {
			t.Fatalf("software encoder args = %#v", args)
		}
	}
	software := encoderCandidates("linux", config.ExportEncoderSoftware, 1, 1000)
	if len(software) != 1 || software[0].name != "libx264" {
		t.Fatalf("software-only candidates = %#v", software)
	}
}
