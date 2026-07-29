package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestDirectoryPickerUsesLinuxSystemDialog(t *testing.T) {
	var gotName string
	var gotArgs []string
	picker := &DirectoryPicker{
		platform: "linux",
		lookPath: func(name string) (string, error) {
			if name == "zenity" {
				return "/usr/bin/zenity", nil
			}
			return "", errors.New("not found")
		},
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName, gotArgs = name, args
			return []byte("/home/user/Videos\n"), nil
		},
	}
	directory, err := picker.Select(context.Background(), "/home/user")
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/home/user/Videos" || gotName != "/usr/bin/zenity" {
		t.Fatalf("Select() = (%q, %q), want selected directory and zenity", directory, gotName)
	}
	wantArgs := []string{"--file-selection", "--directory", "--title=Video Recorder", "--filename=/home/user/"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("zenity arguments = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestDirectoryPickerCancellation(t *testing.T) {
	picker := &DirectoryPicker{
		platform: "darwin",
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("execution error: User canceled. (-128)"), errors.New("exit status 1")
		},
	}
	if _, err := picker.Select(context.Background(), ""); !errors.Is(err, ErrDirectorySelectionCanceled) {
		t.Fatalf("Select() error = %v, want cancellation", err)
	}
}

func TestDirectoryPickerRequiresAbsolutePath(t *testing.T) {
	picker := &DirectoryPicker{
		platform: "darwin",
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("relative/path"), nil
		},
	}
	if _, err := picker.Select(context.Background(), ""); err == nil {
		t.Fatal("Select() accepted a relative directory")
	}
}

func TestDirectoryPickerRejectsConcurrentSelection(t *testing.T) {
	picker := NewDirectoryPicker()
	picker.mu.Lock()
	defer picker.mu.Unlock()
	if _, err := picker.Select(context.Background(), ""); !errors.Is(err, ErrDirectorySelectionBusy) {
		t.Fatalf("Select() error = %v, want busy", err)
	}
}

func TestDirectoryPickerReportsMissingLinuxDialog(t *testing.T) {
	picker := &DirectoryPicker{
		platform: "linux",
		lookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
	if _, err := picker.Select(context.Background(), ""); err == nil || errors.Is(err, ErrDirectorySelectionCanceled) {
		t.Fatalf("Select() error = %v, want unavailable picker error", err)
	}
}
