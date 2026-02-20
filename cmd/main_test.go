package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bmaio-redhat/k8s-healer/pkg/daemon"
)

func Test_summaryPathFromPIDFile(t *testing.T) {
	tests := []struct {
		name    string
		pidFile string
		wantDir string
		wantName string
	}{
		{
			name:    "default empty uses default PID dir",
			pidFile: "",
			wantDir: filepath.Dir(daemon.DefaultPIDFile),
			wantName: "k8s-healer-summary.out",
		},
		{
			name:    "custom PID file path",
			pidFile: "/var/run/healer.pid",
			wantDir: "/var/run",
			wantName: "k8s-healer-summary.out",
		},
		{
			name:    "PID file in subdir",
			pidFile: "/tmp/run/k8s-healer.pid",
			wantDir: filepath.Join("/tmp", "run"),
			wantName: "k8s-healer-summary.out",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summaryPathFromPIDFile(tt.pidFile)
			dir := filepath.Dir(got)
			base := filepath.Base(got)
			if dir != tt.wantDir {
				t.Errorf("summaryPathFromPIDFile(%q) dir = %q, want %q", tt.pidFile, dir, tt.wantDir)
			}
			if base != tt.wantName {
				t.Errorf("summaryPathFromPIDFile(%q) base = %q, want %q", tt.pidFile, base, tt.wantName)
			}
		})
	}
}

func TestSummaryPath(t *testing.T) {
	tests := []struct {
		name     string
		pidFile  string
		wantPath string
	}{
		{
			name:     "custom PID file path",
			pidFile:  "/var/run/healer.pid",
			wantPath: filepath.Join("/var/run", "k8s-healer-summary.out"),
		},
		{
			name:     "empty PID file uses default",
			pidFile:  "",
			wantPath: filepath.Join(filepath.Dir(daemon.DefaultPIDFile), "k8s-healer-summary.out"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SummaryPath(tt.pidFile)
			if got != tt.wantPath {
				t.Errorf("SummaryPath(%q) = %q, want %q", tt.pidFile, got, tt.wantPath)
			}
		})
	}
}

func TestWriteSummaryToPath_CreatesDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()
	// Path with multiple nonexistent parent segments (daemon writes ASCII table to .out next to PID file)
	path := filepath.Join(dir, "a", "b", "c", "k8s-healer-summary.out")
	content := []byte("CRD resource summary (created)\n\n+-...-+\n| Resource type | Count |\n")

	err := writeSummaryToPath(path, content)
	if err != nil {
		t.Fatalf("writeSummaryToPath() error = %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("writeSummaryToPath() did not create file at %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(data) != string(content) {
		t.Errorf("writeSummaryToPath() wrote %q, want %q", data, content)
	}
	// Parent directories should exist
	for _, p := range []string{filepath.Join(dir, "a"), filepath.Join(dir, "a", "b"), filepath.Join(dir, "a", "b", "c")} {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("expected directory %q to exist and be a dir (err=%v)", p, err)
		}
	}
}
