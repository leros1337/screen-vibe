package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	// Check interval in seconds
	checkInterval = 5
	// Default maximum file size in megabytes (1GB)
	defaultMaxFileSizeMB = 1024
)

// Global variables for command line settings
var maxFileSizeBytes int64
var manualDisplayID string
var fps int
var useH264 bool
var preset string
var bitrate int

func main() {
	// Parse command line flags
	maxFileSizeMB := flag.Int("size", defaultMaxFileSizeMB, "Maximum file size in megabytes (default: 1024 MB / 1 GB)")
	displayID := flag.String("display", "", "Display ID to record (default: auto-detect)")
	listFlag := flag.Bool("list", false, "List available displays and exit")
	fpsFlag := flag.Int("fps", 5, "Frames per second for recording (default: 5)")
	h264Flag := flag.Bool("h264", false, "Use H.264 codec instead of H.265/HEVC (better compatibility)")
	presetFlag := flag.String("preset", "medium", "Encoding preset (ultrafast, superfast, veryfast, faster, fast, medium, slow, slower)")
	bitrateFlag := flag.Int("bitrate", 700, "Video bitrate in kbit/s (default: 700)")
	flag.Parse()

	// Store command settings in global variables
	fps = *fpsFlag
	useH264 = *h264Flag
	preset = *presetFlag
	bitrate = *bitrateFlag

	// Check if we only need to show available displays
	if *listFlag {
		fmt.Println("Available displays that can be used with the -display flag:")
		showAvailableDisplays()
		return
	}

	// Convert MB to bytes
	maxFileSizeBytes = int64(*maxFileSizeMB) * 1024 * 1024

	// Store display ID in global variable if provided
	if *displayID != "" {
		manualDisplayID = *displayID
	}

	// Setup signal handling for graceful termination
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Check ffmpeg availability
	if !isFFmpegAvailable() {
		fmt.Println("Error: ffmpeg is not installed or not in PATH.")
		os.Exit(1)
	}

	fmt.Printf("Recording with maximum file size of %s\n", formatFileSize(maxFileSizeBytes))
	fmt.Printf("Recording at %d frames per second\n", fps)
	fmt.Printf("Video bitrate: %d kbit/s\n", bitrate)

	// Show codec and preset info
	if useH264 {
		fmt.Println("Using H.264 codec for better compatibility")
	} else {
		fmt.Println("Using H.265/HEVC codec for better compression")
	}
	fmt.Printf("Encoding preset: %s\n", preset)

	// Show available displays if we're not using a manual display ID
	if manualDisplayID == "" {
		showAvailableDisplays()
	} else {
		fmt.Printf("Using manually specified display: %s\n", manualDisplayID)
	}

	fmt.Println("Press Ctrl+C to stop recording gracefully")

	// Start recording session, which handles restarts if files get too large
	go startRecordingSession(done, sigs)

	// Wait for done signal
	<-done
	fmt.Println("Recording complete")
}

func startRecordingSession(done chan bool, sigs chan os.Signal) {
	var stopRecording = make(chan bool, 1)
	var recordingDone = make(chan bool, 1)

	// Start initial recording
	go startNewRecording(stopRecording, recordingDone)

	for {
		select {
		case <-recordingDone:
			// Normal recording completion - start a new one
			go startNewRecording(stopRecording, recordingDone)
		case sig := <-sigs:
			// User requested termination
			fmt.Printf("Received signal %v, stopping recording...\n", sig)
			stopRecording <- true
			<-recordingDone // Wait for recording to finish
			done <- true
			return
		}
	}
}

func startNewRecording(stopRecording chan bool, recordingDone chan bool) {
	// Create output directory if it doesn't exist
	outputDir := "output"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("Error creating output directory: %v\n", err)
		recordingDone <- true
		return
	}

	// Prepare output file and log file names
	baseName := time.Now().Format("2006-01-02_15-04-05")
	videoFile := filepath.Join(outputDir, baseName+".mkv")
	logFile := filepath.Join(outputDir, baseName+".log")

	// Set up slog logger and log file with DEBUG level
	logWriter := mustCreateFile(logFile)
	handlerOpts := &slog.HandlerOptions{Level: slog.LevelDebug}
	log := slog.New(slog.NewTextHandler(logWriter, handlerOpts))
	log.Info("Starting screen recording", "output", videoFile)
	log.Info("Recording settings", "fps", fps, "bitrate", fmt.Sprintf("%d kbit/s", bitrate), "maxSize", formatFileSize(maxFileSizeBytes))

	// Detect hardware encoder
	encoder, device := detectHardwareEncoder(log)
	log.Info("Selected encoder", "encoder", encoder, "device", device)

	// Build ffmpeg command
	cmd := buildFFmpegCommand(encoder, device, videoFile, log)
	log.Info("Running ffmpeg", "cmd", cmd.String())

	// Set up pipes for ffmpeg IO
	stderrPipe, _ := cmd.StderrPipe()

	// Create stdin pipe before starting the process
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		log.Error("Failed to get stdin pipe for ffmpeg", "error", err)
		stdinPipe = nil // Ensure it's nil if there was an error
	}

	// Stdout can go directly to console
	cmd.Stdout = os.Stdout

	// Start the command
	if err := cmd.Start(); err != nil {
		log.Error("Failed to start ffmpeg", "error", err)
		recordingDone <- true
		return
	}

	// Process stderr for progress updates
	ffmpegOutputDone := make(chan bool, 1)
	go processFFmpegOutput(stderrPipe, log, ffmpegOutputDone)

	// Start file size monitoring
	go monitorFileSize(videoFile, stopRecording, log)

	// Wait for stop signal or command to finish
	stopChan := make(chan struct{})
	go func() {
		// Wait for stop signal directly (no select needed for single case)
		<-stopRecording
		log.Info("Stop signal received, gracefully terminating ffmpeg...")

		if stdinPipe != nil {
			// Use the 'q' keypress method for graceful shutdown (preferred method)
			log.Info("Sending 'q' command to ffmpeg for graceful shutdown")

			// Send a single 'q' and flush
			if _, err := stdinPipe.Write([]byte("q\n")); err != nil {
				log.Error("Failed to send 'q' command", "error", err)
			}

			// Give ffmpeg up to 10 seconds to finish gracefully
			// The longer timeout ensures the file is properly finalized
			gracefulTimeout := time.NewTimer(10 * time.Second)

			log.Info("Waiting for ffmpeg to finalize the video file...")

			select {
			case <-gracefulTimeout.C:
				log.Warn("Graceful shutdown timed out after 10 seconds")
				// Still don't send additional signals - let ffmpeg finish
				// This is critical for proper file finalization
			case <-stopChan:
				log.Info("ffmpeg terminated gracefully")
				gracefulTimeout.Stop()
				return
			}
		}
	}()

	// Wait for ffmpeg to exit
	err = cmd.Wait()
	close(stopChan) // Signal that ffmpeg has terminated

	if err != nil {
		// Check for expected exit codes during graceful shutdown
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			// ffmpeg may return various non-zero exit codes during normal termination
			if exitCode == 255 || exitCode == 0 || exitCode == 1 {
				log.Info("ffmpeg exited with expected code", "code", exitCode)
			} else {
				log.Error("ffmpeg exited with unexpected error code", "code", exitCode, "error", err)
			}
		} else {
			log.Error("ffmpeg exited with error", "error", err)
		}
	} else {
		log.Info("Recording finished successfully")
	}

	<-ffmpegOutputDone // Wait for output processing to finish
	logWriter.Close()
	recordingDone <- true
}

// monitorFileSize checks output file size periodically and signals to stop
// if it exceeds the maximum size limit
func monitorFileSize(filePath string, stopRecording chan bool, log *slog.Logger) {
	ticker := time.NewTicker(checkInterval * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			log.Warn("Could not check file size", "error", err)
			continue
		}

		if fileInfo.Size() >= maxFileSizeBytes {
			// Format sizes in MB or GB for more readable logs
			sizeStr := formatFileSize(fileInfo.Size())
			limitStr := formatFileSize(maxFileSizeBytes)
			log.Info(fmt.Sprintf("File %s exceeded size limit of %s (current size: %s), gracefully stopping and starting new recording",
				filePath, limitStr, sizeStr))

			// Signal to stop recording - this will use our improved graceful shutdown
			stopRecording <- true
			return
		}
	}
}

// formatFileSize converts bytes to a human-readable format (KB, MB, GB)
func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

// processFFmpegOutput reads ffmpeg stderr output, handles carriage returns,
// logs each line, and prints it to console
func processFFmpegOutput(r io.Reader, log *slog.Logger, done chan bool) {
	// Use a buffered reader instead of a scanner to handle carriage returns
	reader := bufio.NewReader(r)
	var line strings.Builder

	for {
		b, err := reader.ReadByte()
		if err != nil {
			if err != io.EOF {
				log.Error("Error reading ffmpeg output", "error", err)
			}
			break
		}

		// Handle carriage return (progress updates)
		if b == '\r' {
			// If we have content, log it and print to console
			if line.Len() > 0 {
				s := line.String()
				fmt.Println(s)
				log.Debug(s)
				line.Reset()
			}
			continue
		}

		// Handle newline
		if b == '\n' {
			// If we have content, log it and print to console
			if line.Len() > 0 {
				s := line.String()
				fmt.Println(s)
				log.Debug(s)
				line.Reset()
			}
			continue
		}

		// Add byte to the current line
		line.WriteByte(b)
	}

	// Log any remaining content
	if line.Len() > 0 {
		s := line.String()
		fmt.Println(s)
		log.Debug(s)
	}

	done <- true
}

func isFFmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func mustCreateFile(name string) *os.File {
	f, err := os.Create(name)
	if err != nil {
		panic(err)
	}
	return f
}

func detectHardwareEncoder(log *slog.Logger) (encoder, device string) {
	osType := runtime.GOOS

	// Log codec choice
	if useH264 {
		log.Info("Using H.264 codec for better compatibility")
	} else {
		log.Info("Using H.265/HEVC codec (higher compression)")
	}

	// If manual display ID is set, use it
	if manualDisplayID != "" {
		log.Info("Using manually specified display", "id", manualDisplayID)

		// Select appropriate encoder based on OS and codec choice
		if osType == "darwin" {
			if useH264 {
				return "h264_videotoolbox", manualDisplayID
			}
			return "hevc_videotoolbox", manualDisplayID
		} else if osType == "windows" {
			// For Windows, select encoder based on GPU and codec choice
			var encoder string

			if useH264 {
				// H.264 encoders
				encoder = "libx264" // Default to CPU
				if hasNvidiaGPU() {
					encoder = "h264_nvenc"
				} else if hasIntelGPU() {
					encoder = "h264_qsv"
				} else if hasAMDGPU() {
					encoder = "h264_amf"
				}
			} else {
				// H.265 encoders
				encoder = "libx265" // Default to CPU
				if hasNvidiaGPU() {
					encoder = "hevc_nvenc"
				} else if hasIntelGPU() {
					encoder = "hevc_qsv"
				} else if hasAMDGPU() {
					encoder = "hevc_amf"
				}
			}
			return encoder, manualDisplayID
		} else if osType == "linux" {
			// For Linux, select encoder based on GPU and codec choice
			var encoder string

			if useH264 {
				// H.264 encoders
				encoder = "libx264" // Default to CPU
				if hasNvidiaGPU() {
					encoder = "h264_nvenc"
				} else if hasIntelGPU() {
					encoder = "h264_qsv"
				} else if hasAMDGPU() {
					encoder = "h264_amf"
				}
			} else {
				// H.265 encoders
				encoder = "libx265" // Default to CPU
				if hasNvidiaGPU() {
					encoder = "hevc_nvenc"
				} else if hasIntelGPU() {
					encoder = "hevc_qsv"
				} else if hasAMDGPU() {
					encoder = "hevc_amf"
				}
			}
			return encoder, manualDisplayID
		}
	}

	// Auto-detect display if manual ID not provided
	// macOS: use videotoolbox
	if osType == "darwin" {
		device := getMacOSMainDisplayID(log)
		if useH264 {
			return "h264_videotoolbox", device
		}
		return "hevc_videotoolbox", device
	}

	// Windows: try NVENC, QSV, AMF, else fallback
	if osType == "windows" {
		device := getWindowsMainDisplayID(log)
		var encoder string

		if useH264 {
			// H.264 encoders
			encoder = "libx264" // Default to CPU
			if hasNvidiaGPU() {
				encoder = "h264_nvenc"
				log.Info("Detected NVIDIA GPU, using hardware acceleration", "encoder", encoder)
			} else if hasIntelGPU() {
				encoder = "h264_qsv"
				log.Info("Detected Intel GPU, using QuickSync acceleration", "encoder", encoder)
			} else if hasAMDGPU() {
				encoder = "h264_amf"
				log.Info("Detected AMD GPU, using AMF acceleration", "encoder", encoder)
			} else {
				log.Info("No supported GPU detected, using CPU encoding", "encoder", encoder)
			}
		} else {
			// H.265 encoders
			encoder = "libx265" // Default to CPU
			if hasNvidiaGPU() {
				encoder = "hevc_nvenc"
				log.Info("Detected NVIDIA GPU, using hardware acceleration", "encoder", encoder)
			} else if hasIntelGPU() {
				encoder = "hevc_qsv"
				log.Info("Detected Intel GPU, using QuickSync acceleration", "encoder", encoder)
			} else if hasAMDGPU() {
				encoder = "hevc_amf"
				log.Info("Detected AMD GPU, using AMF acceleration", "encoder", encoder)
			} else {
				log.Info("No supported GPU detected, using CPU encoding", "encoder", encoder)
			}
		}

		return encoder, device
	}

	// Linux: try NVENC, VAAPI, else fallback
	if osType == "linux" {
		if useH264 {
			if hasNvidiaGPU() {
				return "h264_nvenc", "0"
			}
			if hasIntelGPU() {
				return "h264_qsv", "0"
			}
			if hasAMDGPU() {
				return "h264_amf", "0"
			}
			return "libx264", "0"
		} else {
			if hasNvidiaGPU() {
				return "hevc_nvenc", "0"
			}
			if hasIntelGPU() {
				return "hevc_qsv", "0"
			}
			if hasAMDGPU() {
				return "hevc_amf", "0"
			}
			return "libx265", "0"
		}
	}

	// Fallback to CPU with appropriate codec
	if useH264 {
		return "libx264", "0"
	}
	return "libx265", "0"
}

func buildFFmpegCommand(encoder, device, videoFile string, log *slog.Logger) *exec.Cmd {
	osType := runtime.GOOS
	var args []string

	// Convert fps to string for ffmpeg arguments
	fpsStr := fmt.Sprintf("%d", fps)

	// Calculate GOP size based on formula GOP = fps × 2
	gopSize := fps * 2

	log.Info("Setting GOP size", "fps", fps, "gopSize", gopSize)

	// Create strings for bitrate settings
	bitrateStr := fmt.Sprintf("%dk", bitrate)
	maxrateStr := fmt.Sprintf("%dk", bitrate*2) // Max rate is 2x the target bitrate
	bufsizeStr := fmt.Sprintf("%dk", bitrate*3) // Buffer size is 3x the target bitrate

	log.Info("Setting bitrate parameters", "bitrate", bitrateStr, "maxrate", maxrateStr, "bufsize", bufsizeStr)

	if osType == "darwin" {
		// macOS screen capture, use compatible pixel format for input
		args = []string{
			"-f", "avfoundation",
			"-framerate", fpsStr,
			"-pix_fmt", "uyvy422",
			"-i", device,
			"-c:v", encoder,
			"-r", fpsStr, // Explicit output framerate
			"-g", fmt.Sprintf("%d", gopSize), // GOP size based on fps × 2
			"-b:v", bitrateStr,
			"-maxrate", maxrateStr,
			"-bufsize", bufsizeStr,
			"-pix_fmt", "yuv420p", // More compatible pixel format
			"-profile:v", "main",
			"-an", // No audio
			videoFile,
		}
	} else if osType == "windows" {
		// Windows screen capture
		baseArgs := []string{
			"-f", "gdigrab",
			"-framerate", fpsStr,
			"-i", device,
			"-c:v", encoder,
			"-r", fpsStr, // Explicit output framerate
			"-g", fmt.Sprintf("%d", gopSize), // GOP size based on fps × 2
			"-pix_fmt", "yuv420p", // More compatible pixel format
			"-preset", preset, // Use command line preset
			"-b:v", bitrateStr,
			"-maxrate", maxrateStr,
			"-bufsize", bufsizeStr,
			"-profile:v", "main",
		}

		// Special options for Windows depending on codec
		if strings.Contains(encoder, "264") {
			// H.264 specific options
			baseArgs = append(baseArgs, "-level", "4.1") // Good compatibility level
			if strings.Contains(encoder, "nvenc") {
				// NVIDIA specific options
				baseArgs = append(baseArgs, "-rc:v", "vbr_hq")
			}
		} else {
			// H.265/HEVC specific options
			if !strings.Contains(encoder, "amf") && !strings.Contains(encoder, "qsv") {
				// Add tag for better compatibility except for AMF and QSV encoders
				baseArgs = append(baseArgs, "-tag:v", "hvc1")
			}
		}

		// Complete the argument list
		baseArgs = append(baseArgs,
			"-an", // No audio
			videoFile,
		)

		args = baseArgs
	} else {
		// Linux (X11) screen capture
		displayInput := ":0.0" // Default display
		if manualDisplayID != "" {
			displayInput = manualDisplayID
		}

		args = []string{
			"-f", "x11grab",
			"-framerate", fpsStr,
			"-i", displayInput,
			"-c:v", encoder,
			"-r", fpsStr, // Explicit output framerate
			"-g", fmt.Sprintf("%d", gopSize), // GOP size based on fps × 2
			"-pix_fmt", "yuv420p", // More compatible pixel format
			"-b:v", bitrateStr,
			"-maxrate", maxrateStr,
			"-bufsize", bufsizeStr,
			"-profile:v", "main",
			"-an", // No audio
			videoFile,
		}
	}
	return exec.Command("ffmpeg", args...)
}

func getMacOSMainDisplayID(log *slog.Logger) string {
	outputDir := "output"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Warn("Could not create output directory", "error", err)
	}

	deviceFile := filepath.Join(outputDir, "avfoundation_devices.txt")
	// Always (re)create the device list file on program start
	cmd := exec.Command("ffmpeg", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	f, err := os.Create(deviceFile)
	if err != nil {
		log.Warn("Could not create device list file, defaulting to 2:none", "error", err)
		return "2:none"
	}
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Run(); err != nil {
		log.Warn("Could not run ffmpeg for device list, defaulting to 2:none", "error", err)
		return "2:none"
	}
	f.Close()

	// Now parse the file for the correct display device
	file, err := os.Open(deviceFile)
	if err != nil {
		log.Warn("Could not open device list file, defaulting to 2:none", "error", err)
		return "2:none"
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	mainDisplayIdx := "2" // fallback
	deviceRe := regexp.MustCompile(`\[([0-9]+)\] (.*)`)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "AVFoundation video devices") {
			for scanner.Scan() {
				line = scanner.Text()
				if strings.Contains(line, "AVFoundation audio devices") {
					break
				}
				if m := deviceRe.FindStringSubmatch(line); m != nil {
					idx, name := m[1], m[2]
					if strings.Contains(strings.ToLower(name), "capture screen") {
						mainDisplayIdx = idx
						log.Info("Selected main display device", "index", idx, "name", name)
						break
					}
				}
			}
			break
		}
	}
	return mainDisplayIdx + ":none"
}

func getWindowsMainDisplayID(log *slog.Logger) string {
	// For Windows, we can use:
	// - "desktop" for full desktop
	// - "title=Window Title" for specific window
	// - "hwnd=123456" for window handle

	// List available windows for the log file
	outputDir := "output"
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Warn("Could not create output directory", "error", err)
	}

	// Use PowerShell to get window titles (helps user identify windows)
	cmd := exec.Command("powershell", "-Command",
		"Get-Process | Where-Object {$_.MainWindowTitle -ne \"\"} | Select-Object MainWindowTitle | Format-Table -AutoSize")

	// Capture window information to a file
	windowsFile := filepath.Join(outputDir, "windows_list.txt")
	f, err := os.Create(windowsFile)
	if err == nil {
		cmd.Stdout = f
		cmd.Run() // Ignore errors as this is just informational
		f.Close()
		log.Info("Available Windows saved to", "file", windowsFile)
	}

	return "desktop" // Default to full desktop capture
}

func hasNvidiaGPU() bool {
	// Check for NVIDIA GPU presence
	if runtime.GOOS == "linux" {
		// Try to run nvidia-smi to detect NVIDIA GPU
		cmd := exec.Command("nvidia-smi")
		if err := cmd.Run(); err == nil {
			return true
		}

		// Alternative check for NVIDIA GPUs by looking at PCI devices
		cmd = exec.Command("lspci")
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), "NVIDIA") {
			return true
		}
	} else if runtime.GOOS == "windows" {
		// Check Windows registry or WMI for NVIDIA devices
		cmd := exec.Command("wmic", "path", "win32_VideoController", "get", "name")
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), "NVIDIA") {
			return true
		}
	}
	return false
}

func hasIntelGPU() bool {
	// Check for Intel GPU presence
	if runtime.GOOS == "linux" {
		// Check for Intel GPUs in PCI devices
		cmd := exec.Command("lspci")
		output, err := cmd.Output()
		if err == nil && (strings.Contains(string(output), "Intel Corporation") &&
			(strings.Contains(string(output), "VGA") ||
				strings.Contains(string(output), "Graphics"))) {
			return true
		}
	} else if runtime.GOOS == "windows" {
		// Check Windows registry or WMI for Intel graphics devices
		cmd := exec.Command("wmic", "path", "win32_VideoController", "get", "name")
		output, err := cmd.Output()
		if err == nil && (strings.Contains(string(output), "Intel") &&
			strings.Contains(string(output), "Graphics")) {
			return true
		}
	}
	return false
}

func hasAMDGPU() bool {
	// Check for AMD GPU presence
	if runtime.GOOS == "linux" {
		// Check for AMD GPUs in PCI devices
		cmd := exec.Command("lspci")
		output, err := cmd.Output()
		if err == nil && (strings.Contains(string(output), "AMD") ||
			strings.Contains(string(output), "ATI") ||
			strings.Contains(string(output), "Radeon")) {
			return true
		}
	} else if runtime.GOOS == "windows" {
		// Check Windows registry or WMI for AMD graphics devices
		cmd := exec.Command("wmic", "path", "win32_VideoController", "get", "name")
		output, err := cmd.Output()
		if err == nil && (strings.Contains(string(output), "AMD") ||
			strings.Contains(string(output), "Radeon")) {
			return true
		}
	}
	return false
}

// showAvailableDisplays shows a list of available displays that can be recorded
func showAvailableDisplays() {
	osType := runtime.GOOS
	if osType == "darwin" {
		// Create temp dir for device list if needed
		outputDir := "output"
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			fmt.Printf("Warning: Could not create output directory: %v\n", err)
		}

		// Get the list of AVFoundation devices
		deviceFile := filepath.Join(outputDir, "avfoundation_devices.txt")
		cmd := exec.Command("ffmpeg", "-f", "avfoundation", "-list_devices", "true", "-i", "")

		// Capture the output to the file instead of displaying it directly
		f, err := os.Create(deviceFile)
		if err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
			cmd.Run() // We expect this to fail with a non-zero exit code
			f.Close()
		}

		fmt.Println("\nAvailable displays for recording:")
		fmt.Println("--------------------------------")

		// Parse the device list from stderr output that was printed
		file, err := os.Open(deviceFile)
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			inVideoSection := false
			deviceRe := regexp.MustCompile(`\[([0-9]+)\] (.*)`)

			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "AVFoundation video devices") {
					inVideoSection = true
					continue
				}
				if inVideoSection {
					if strings.Contains(line, "AVFoundation audio devices") {
						break
					}
					if m := deviceRe.FindStringSubmatch(line); m != nil {
						idx, name := m[1], m[2]
						// Highlight screen capture devices
						if strings.Contains(strings.ToLower(name), "screen") ||
							strings.Contains(strings.ToLower(name), "display") ||
							strings.Contains(strings.ToLower(name), "capture") {
							fmt.Printf("  * %s: %s (recommended for screen recording)\n", idx, name)
						} else {
							fmt.Printf("  - %s: %s\n", idx, name)
						}
					}
				}
			}
			fmt.Println("--------------------------------")
			fmt.Println("To select a specific display, use the -display flag (e.g., -display '2:none')")
			fmt.Println()
		} else {
			fmt.Printf("Warning: Could not read device list file: %v\n", err)
		}
	} else if osType == "windows" {
		fmt.Println("\nAvailable displays for Windows:")
		fmt.Println("--------------------------------")
		fmt.Println("  - desktop: Full desktop (all screens)")
		fmt.Println("  - title=Window Title: Specific window by title")
		fmt.Println("--------------------------------")
		fmt.Println("To select a specific display, use the -display flag (e.g., -display 'desktop')")
	} else { // Linux
		fmt.Println("\nAvailable displays for Linux:")
		fmt.Println("--------------------------------")
		fmt.Println("  - :0.0: Primary display")
		fmt.Println("  - :0.0+1920,0: Second monitor (adjust offset as needed)")
		fmt.Println("--------------------------------")
		fmt.Println("To select a specific display, use the -display flag (e.g., -display ':0.0')")
	}
}
