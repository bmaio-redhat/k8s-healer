package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultPIDFile is the default path for the PID file
	DefaultPIDFile = "/tmp/k8s-healer.pid"
	// DefaultLogFile is the default path for the log file
	DefaultLogFile = "/tmp/k8s-healer.log"
)

// DaemonConfig holds configuration for daemon mode
type DaemonConfig struct {
	PIDFile string
	LogFile string
	WorkDir string
	Args    []string
	Env     []string
}

// StartDaemon forks the process and runs it as a daemon
func StartDaemon(config DaemonConfig) error {
	// Check if already running
	if IsRunning(config.PIDFile) {
		return fmt.Errorf("k8s-healer is already running (PID file exists: %s)", config.PIDFile)
	}

	// Get the executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Prepare the command
	cmd := exec.Command(execPath, config.Args...)
	cmd.Env = append(os.Environ(), config.Env...)

	if config.WorkDir != "" {
		cmd.Dir = config.WorkDir
	}

	// Redirect stdout and stderr to log file
	if config.LogFile != "" {
		logFile, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	// Set process attributes for daemonization
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create new session
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Write PID file
	if err := WritePIDFile(config.PIDFile, cmd.Process.Pid); err != nil {
		// Try to kill the process if we can't write PID file
		cmd.Process.Kill()
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	fmt.Printf("k8s-healer started as daemon (PID: %d)\n", cmd.Process.Pid)
	fmt.Printf("Log file: %s\n", config.LogFile)
	fmt.Printf("PID file: %s\n", config.PIDFile)

	return nil
}

// StopDaemon stops the running daemon
func StopDaemon(pidFile string) error {
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("daemon is not running: PID file not found at %s. If you started the daemon with a custom --pid-file, run: k8s-healer stop --pid-file <path>. To stop any running k8s-healer process: pkill -f k8s-healer", pidFile)
		}
		return fmt.Errorf("daemon is not running: %w", err)
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		// PID file exists but process doesn't - clean up stale PID file
		os.Remove(pidFile)
		return fmt.Errorf("process %d not found, removed stale PID file", pid)
	}

	// Send SIGTERM for graceful shutdown
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead
		os.Remove(pidFile)
		return fmt.Errorf("failed to send SIGTERM to process %d: %w", pid, err)
	}

	// Wait a bit and check if process is still running
	time.Sleep(2 * time.Second)

	// Check if process is still alive by sending signal 0
	if err := process.Signal(syscall.Signal(0)); err == nil {
		// Process still running, force kill
		fmt.Printf("Process %d did not respond to SIGTERM, sending SIGKILL...\n", pid)
		process.Kill()
	}

	// Remove PID file
	os.Remove(pidFile)
	fmt.Printf("k8s-healer daemon stopped (PID: %d)\n", pid)
	return nil
}

// StatusDaemon checks if the daemon is running
func StatusDaemon(pidFile string) error {
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		fmt.Println("k8s-healer is not running")
		return err
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("k8s-healer is not running (stale PID file: %s)\n", pidFile)
		os.Remove(pidFile)
		return err
	}

	// Send signal 0 to check if process is alive
	if err := process.Signal(syscall.Signal(0)); err != nil {
		fmt.Printf("k8s-healer is not running (process %d not found)\n", pid)
		os.Remove(pidFile)
		return err
	}

	fmt.Printf("k8s-healer is running (PID: %d)\n", pid)
	return nil
}

// IsRunning checks if the daemon is running by checking the PID file
func IsRunning(pidFile string) bool {
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		return false
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Send signal 0 to check if process is alive
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return false
	}

	return true
}

// WritePIDFile writes the PID to a file
func WritePIDFile(pidFile string, pid int) error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(pidFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create PID file directory: %w", err)
	}

	file, err := os.OpenFile(pidFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create PID file: %w", err)
	}
	defer file.Close()

	_, err = fmt.Fprintf(file, "%d\n", pid)
	return err
}

// ReadPIDFile reads the PID from a file
func ReadPIDFile(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in file: %w", err)
	}

	return pid, nil
}

// RedirectOutput redirects stdout and stderr to a log file
func RedirectOutput(logFile string) error {
	if logFile == "" {
		return nil
	}

	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	// Redirect stdout and stderr
	os.Stdout = file
	os.Stderr = file

	return nil
}
