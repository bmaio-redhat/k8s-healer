package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWritePIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")
	pid := 12345

	err := WritePIDFile(pidFile, pid)
	if err != nil {
		t.Fatalf("WritePIDFile() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		t.Fatalf("PID file was not created")
	}

	// Verify content
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	expected := "12345\n"
	if string(data) != expected {
		t.Errorf("PID file content = %q, want %q", string(data), expected)
	}
}

func TestReadPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")
	expectedPID := 12345

	// Write PID file
	err := os.WriteFile(pidFile, []byte("12345\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	// Read PID file
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		t.Fatalf("ReadPIDFile() error = %v", err)
	}

	if pid != expectedPID {
		t.Errorf("ReadPIDFile() = %v, want %v", pid, expectedPID)
	}
}

func TestReadPIDFile_InvalidContent(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")

	// Write invalid PID file
	err := os.WriteFile(pidFile, []byte("not-a-number\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	// Read PID file should fail
	_, err = ReadPIDFile(pidFile)
	if err == nil {
		t.Error("ReadPIDFile() expected error for invalid content, got nil")
	}
}

func TestReadPIDFile_NonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid")

	// Read nonexistent PID file should fail
	_, err := ReadPIDFile(pidFile)
	if err == nil {
		t.Error("ReadPIDFile() expected error for nonexistent file, got nil")
	}
}

func TestReadPIDFile_Whitespace(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")
	expectedPID := 12345

	// Write PID file with whitespace
	err := os.WriteFile(pidFile, []byte("  12345  \n"), 0644)
	if err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	// Read PID file should handle whitespace
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		t.Fatalf("ReadPIDFile() error = %v", err)
	}

	if pid != expectedPID {
		t.Errorf("ReadPIDFile() = %v, want %v", pid, expectedPID)
	}
}

func TestWritePIDFile_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "subdir", "test.pid")
	pid := 12345

	err := WritePIDFile(pidFile, pid)
	if err != nil {
		t.Fatalf("WritePIDFile() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		t.Fatalf("PID file was not created")
	}
}

// TestWritePIDFile_RelativePathBecomesAbsolute verifies that a relative PID file path is resolved to absolute.
func TestWritePIDFile_RelativePathBecomesAbsolute(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer os.Chdir(origWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	pid := 54321
	err = WritePIDFile("custom.pid", pid)
	if err != nil {
		t.Fatalf("WritePIDFile() error = %v", err)
	}

	// File should exist at tmpDir/custom.pid (resolved to absolute)
	absPath := filepath.Join(tmpDir, "custom.pid")
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		t.Fatalf("PID file was not created at resolved path %s", absPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "54321\n" {
		t.Errorf("PID file content = %q, want %q", string(data), "54321\n")
	}
}

// TestSendSignal_EmptyPathReturnsError verifies that SendSignal with empty pid file path returns an error.
func TestSendSignal_EmptyPathReturnsError(t *testing.T) {
	err := SendSignal("", syscall.SIGUSR2)
	if err == nil {
		t.Error("SendSignal() with empty path expected error, got nil")
	}
}

func TestRedirectOutput(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	// Save original stdout
	originalStdout := os.Stdout

	// Redirect output
	err := RedirectOutput(logFile)
	if err != nil {
		t.Fatalf("RedirectOutput() error = %v", err)
	}

	// Verify stdout is redirected
	if os.Stdout == originalStdout {
		t.Error("RedirectOutput() did not redirect stdout")
	}

	// Restore original stdout
	os.Stdout = originalStdout
}

func TestRedirectOutput_EmptyPath(t *testing.T) {
	// Save original stdout
	originalStdout := os.Stdout

	// Redirect with empty path should not error
	err := RedirectOutput("")
	if err != nil {
		t.Fatalf("RedirectOutput() with empty path error = %v", err)
	}

	// Verify stdout is unchanged
	if os.Stdout != originalStdout {
		t.Error("RedirectOutput() with empty path should not redirect stdout")
	}
}
