package service

import (
	"reflect"
	"testing"

	"video-recorder/internal/config"
)

func TestEncoderCandidatesPreferHardwareAndCapSoftwareThreads(t *testing.T) {
	tests := []struct {
		platform     string
		firstDecoder string
		firstEncoder string
	}{
		{platform: "darwin", firstDecoder: "mjpeg_videotoolbox", firstEncoder: "h264_videotoolbox"},
		{platform: "windows", firstDecoder: "mjpeg_qsv", firstEncoder: "h264_qsv"},
		{platform: "linux", firstDecoder: "mjpeg_vaapi", firstEncoder: "h264_vaapi"},
	}
	for _, test := range tests {
		candidates := encoderCandidates(test.platform, config.ExportEncoderAuto, 2, 1000)
		if candidates[0].decoder != test.firstDecoder || candidates[0].name != test.firstEncoder || len(candidates[0].inputArgs) == 0 || candidates[len(candidates)-1].name != "libx264" {
			t.Fatalf("%s candidates = %#v", test.platform, candidates)
		}
		foundSoftwareDecodeFallback := false
		for _, candidate := range candidates[1:] {
			if candidate.decoder == "mjpeg" && candidate.name == test.firstEncoder {
				foundSoftwareDecodeFallback = true
				break
			}
		}
		if !foundSoftwareDecodeFallback {
			t.Fatalf("%s candidates do not fall back to software decoding with %s", test.platform, test.firstEncoder)
		}
		if args := candidates[len(candidates)-1].args; !reflect.DeepEqual(args, []string{"-c:v", "libx264", "-preset", "veryfast", "-b:v", "1000k", "-maxrate", "1100k", "-bufsize", "2000k", "-threads", "2", "-pix_fmt", "yuv420p"}) {
			t.Fatalf("software encoder args = %#v", args)
		}
	}
	software := encoderCandidates("linux", config.ExportEncoderSoftware, 1, 1000)
	if len(software) != 1 || software[0].decoder != "mjpeg" || software[0].name != "libx264" {
		t.Fatalf("software-only candidates = %#v", software)
	}
}
