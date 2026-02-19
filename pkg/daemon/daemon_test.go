package daemon

import (
	"os"
	"path/filepath"
	"strings"
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

// TestSendSignal_NoPIDFile verifies that SendSignal returns an error when the PID file does not exist.
func TestSendSignal_NoPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid")
	err := SendSignal(pidFile, syscall.SIGUSR2)
	if err == nil {
		t.Error("SendSignal() with nonexistent PID file expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("SendSignal() error should mention not running, got: %v", err)
	}
}

// TestSendSignal_CurrentProcess verifies that SendSignal succeeds when the PID file contains the current process (sends SIGUSR2 to self).
func TestSendSignal_CurrentProcess(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "current.pid")
	err := WritePIDFile(pidFile, os.Getpid())
	if err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	defer os.Remove(pidFile)
	err = SendSignal(pidFile, syscall.SIGUSR2)
	if err != nil {
		t.Errorf("SendSignal() to current process expected success, got %v", err)
	}
}

// TestSendSignal_StalePID verifies that SendSignal returns an error when the PID file contains a non-existent or finished process.
func TestSendSignal_StalePID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "stale.pid")
	err := os.WriteFile(pidFile, []byte("999999999\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = SendSignal(pidFile, syscall.SIGUSR2)
	if err == nil {
		t.Error("SendSignal() with stale PID expected error, got nil")
	}
	// Either "process ... not found" or "failed to send signal" (process already finished)
	if err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "send signal") {
		t.Errorf("SendSignal() error should mention process or signal, got: %v", err)
	}
}

// TestStopDaemon_NoPIDFile verifies that StopDaemon returns an error when the PID file does not exist.
func TestStopDaemon_NoPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid")
	err := StopDaemon(pidFile)
	if err == nil {
		t.Error("StopDaemon() with nonexistent PID file expected error, got nil")
	}
	if err != nil {
		if !strings.Contains(err.Error(), "not running") {
			t.Errorf("StopDaemon() error should mention not running, got: %v", err)
		}
		if !strings.Contains(err.Error(), "PID file") {
			t.Errorf("StopDaemon() error should mention PID file, got: %v", err)
		}
	}
}

// TestStopDaemon_StalePIDFile verifies that StopDaemon removes the PID file and returns an error when the process does not exist.
func TestStopDaemon_StalePIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "stale.pid")
	// Use a PID that cannot exist (Linux kernel max PID is typically 2^22 or lower)
	err := os.WriteFile(pidFile, []byte("999999999\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = StopDaemon(pidFile)
	if err == nil {
		t.Error("StopDaemon() with stale PID expected error, got nil")
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("StopDaemon() should remove stale PID file")
	}
}

// TestStatusDaemon_Running verifies that StatusDaemon returns nil when the PID file exists and process is running.
func TestStatusDaemon_Running(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "running.pid")
	err := WritePIDFile(pidFile, os.Getpid())
	if err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	defer os.Remove(pidFile)
	err = StatusDaemon(pidFile)
	if err != nil {
		t.Errorf("StatusDaemon() with running process expected nil, got %v", err)
	}
}

// TestStatusDaemon_NoFile verifies that StatusDaemon returns an error when the PID file does not exist.
func TestStatusDaemon_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid")
	err := StatusDaemon(pidFile)
	if err == nil {
		t.Error("StatusDaemon() with missing file expected error, got nil")
	}
}

// TestStatusDaemon_StalePID verifies that StatusDaemon removes the PID file and returns an error when the process does not exist.
func TestStatusDaemon_StalePID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "stale.pid")
	err := os.WriteFile(pidFile, []byte("999999999\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = StatusDaemon(pidFile)
	if err == nil {
		t.Error("StatusDaemon() with stale PID expected error, got nil")
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("StatusDaemon() should remove stale PID file")
	}
}

// TestIsRunning_NoFile verifies that IsRunning returns false when the PID file does not exist.
func TestIsRunning_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid")
	if IsRunning(pidFile) {
		t.Error("IsRunning() with missing file expected false, got true")
	}
}

// TestIsRunning_CurrentProcess verifies that IsRunning returns true when the PID file contains the current process PID.
func TestIsRunning_CurrentProcess(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "current.pid")
	err := WritePIDFile(pidFile, os.Getpid())
	if err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	defer os.Remove(pidFile)
	if !IsRunning(pidFile) {
		t.Error("IsRunning() with current process PID expected true, got false")
	}
}

// TestIsRunning_StalePID verifies that IsRunning returns false when the PID file contains a non-existent process PID.
func TestIsRunning_StalePID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "stale.pid")
	err := os.WriteFile(pidFile, []byte("999999999\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if IsRunning(pidFile) {
		t.Error("IsRunning() with stale PID expected false, got true")
	}
}

// TestStartDaemon_AlreadyRunning verifies that StartDaemon returns an error when the daemon is already running (PID file exists with current process).
func TestStartDaemon_AlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "already.pid")
	err := WritePIDFile(pidFile, os.Getpid())
	if err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	defer os.Remove(pidFile)
	config := DaemonConfig{PIDFile: pidFile, Args: []string{"start"}}
	err = StartDaemon(config)
	if err == nil {
		t.Error("StartDaemon() when already running expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "already running") {
		t.Errorf("StartDaemon() error should mention already running, got: %v", err)
	}
}

// TestStartDaemon_LogFileOpenFails verifies that StartDaemon returns an error when the log file cannot be opened.
func TestStartDaemon_LogFileOpenFails(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "daemon.pid")
	// Use a path under a non-existent read-only parent to force OpenFile to fail (or use /dev/full which is not a directory)
	logFile := filepath.Join(tmpDir, "nonexistent_subdir", "log")
	config := DaemonConfig{PIDFile: pidFile, LogFile: logFile, Args: []string{"start"}}
	err := StartDaemon(config)
	if err == nil {
		t.Error("StartDaemon() with invalid log file path expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "failed to open log file") {
		t.Errorf("StartDaemon() error should mention failed to open log file, got: %v", err)
	}
}

// TestStartDaemon_Success verifies that StartDaemon starts a child process and writes the PID file.
// It uses the test binary with -test.run=^$ so the child exits immediately without running tests.
func TestStartDaemon_Success(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "daemon.pid")
	logFile := filepath.Join(tmpDir, "daemon.log")
	config := DaemonConfig{
		PIDFile: pidFile,
		LogFile: logFile,
		Args:    []string{"-test.run=^$"},
	}
	err := StartDaemon(config)
	if err != nil {
		t.Fatalf("StartDaemon() unexpected error: %v", err)
	}
	defer os.Remove(pidFile)
	defer os.Remove(logFile)
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		t.Error("StartDaemon() should create PID file")
	}
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("StartDaemon() should create log file")
	}
}

// TestStartDaemon_PIDFileWriteFails verifies that StartDaemon kills the child and returns an error when the PID file cannot be written.
func TestStartDaemon_PIDFileWriteFails(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "daemon.log")
	noWriteDir := filepath.Join(tmpDir, "nowrite")
	if err := os.MkdirAll(noWriteDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(noWriteDir, 0o444); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(noWriteDir, 0755)
	pidFile := filepath.Join(noWriteDir, "daemon.pid")
	config := DaemonConfig{
		PIDFile: pidFile,
		LogFile: logFile,
		Args:    []string{"-test.run=^$"},
	}
	err := StartDaemon(config)
	if err == nil {
		t.Error("StartDaemon() when PID file cannot be written expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "failed to write PID file") {
		t.Errorf("StartDaemon() error should mention failed to write PID file, got: %v", err)
	}
}

// TestStopDaemon_InvalidPIDContent verifies that StopDaemon returns an error when the PID file contains invalid content (not ErrNotExist).
func TestStopDaemon_InvalidPIDContent(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "bad.pid")
	err := os.WriteFile(pidFile, []byte("not-a-number\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = StopDaemon(pidFile)
	if err == nil {
		t.Error("StopDaemon() with invalid PID content expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("StopDaemon() error should mention not running, got: %v", err)
	}
}

// TestSendSignal_InvalidPIDContent verifies that SendSignal returns an error when the PID file contains invalid content.
func TestSendSignal_InvalidPIDContent(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "bad.pid")
	err := os.WriteFile(pidFile, []byte("not-a-number\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err = SendSignal(pidFile, syscall.SIGUSR2)
	if err == nil {
		t.Error("SendSignal() with invalid PID content expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("SendSignal() error should mention not running, got: %v", err)
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

// TestRedirectOutput_OpenFails verifies that RedirectOutput returns an error when the log file cannot be opened.
func TestRedirectOutput_OpenFails(t *testing.T) {
	// Use a path that cannot be created (parent does not exist and is not writable, or use /dev/full for write)
	logFile := filepath.Join(t.TempDir(), "nonexistent_subdir", "log")
	err := RedirectOutput(logFile)
	if err == nil {
		t.Error("RedirectOutput() with invalid path expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "failed to open log file") {
		t.Errorf("RedirectOutput() error should mention failed to open log file, got: %v", err)
	}
}
