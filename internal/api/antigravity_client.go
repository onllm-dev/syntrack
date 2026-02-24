package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Custom errors for Antigravity API failures.
var (
	ErrAntigravityProcessNotFound = errors.New("antigravity: language server process not found")
	ErrAntigravityPortNotFound    = errors.New("antigravity: no listening port found")
	ErrAntigravityConnectionFailed = errors.New("antigravity: connection failed")
	ErrAntigravityInvalidResponse = errors.New("antigravity: invalid response")
	ErrAntigravityNotAuthenticated = errors.New("antigravity: not authenticated")
)

// AntigravityProcessInfo contains detected process information.
type AntigravityProcessInfo struct {
	PID                 int
	CSRFToken           string
	ExtensionServerPort int
	CommandLine         string
}

// AntigravityConnection contains verified connection details.
type AntigravityConnection struct {
	BaseURL   string
	CSRFToken string
	Port      int
	Protocol  string // "https" or "http"
}

// AntigravityClient is a client for the Antigravity local language server API.
type AntigravityClient struct {
	httpClient *http.Client
	connection *AntigravityConnection
	logger     *slog.Logger
}

// AntigravityOption configures an AntigravityClient.
type AntigravityOption func(*AntigravityClient)

// WithAntigravityConnection sets a pre-configured connection (for testing).
func WithAntigravityConnection(conn *AntigravityConnection) AntigravityOption {
	return func(c *AntigravityClient) {
		c.connection = conn
	}
}

// WithAntigravityTimeout sets a custom timeout.
func WithAntigravityTimeout(d time.Duration) AntigravityOption {
	return func(c *AntigravityClient) {
		c.httpClient.Timeout = d
	}
}

// NewAntigravityClient creates a new Antigravity API client.
func NewAntigravityClient(logger *slog.Logger, opts ...AntigravityOption) *AntigravityClient {
	client := &AntigravityClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: 10 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // Allow self-signed certs
				},
			},
		},
		logger: logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// Detect finds and verifies connection to the Antigravity language server.
func (c *AntigravityClient) Detect(ctx context.Context) (*AntigravityConnection, error) {
	if c.connection != nil {
		return c.connection, nil
	}

	c.logger.Debug("detecting Antigravity process")

	// Step 1: Find the process
	processInfo, err := c.detectProcess(ctx)
	if err != nil {
		return nil, err
	}

	c.logger.Debug("found Antigravity process",
		"pid", processInfo.PID,
		"hasToken", processInfo.CSRFToken != "",
		"extensionPort", processInfo.ExtensionServerPort,
	)

	// Step 2: Find listening ports
	ports, err := c.discoverPorts(ctx, processInfo.PID)
	if err != nil || len(ports) == 0 {
		return nil, ErrAntigravityPortNotFound
	}

	c.logger.Debug("found listening ports", "ports", ports)

	// Step 3: Probe ports to find the Connect RPC endpoint
	conn, err := c.probeForConnectAPI(ctx, ports, processInfo.CSRFToken)
	if err != nil {
		return nil, err
	}

	c.connection = conn
	c.logger.Info("connected to Antigravity language server",
		"port", conn.Port,
		"protocol", conn.Protocol,
	)

	return conn, nil
}

// FetchQuotas retrieves the current quota information from the Antigravity API.
func (c *AntigravityClient) FetchQuotas(ctx context.Context) (*AntigravityUserStatusResponse, error) {
	conn, err := c.Detect(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := conn.BaseURL + "/exa.language_server_pb.LanguageServerService/GetUserStatus"

	reqBody := `{"metadata":{"ideName":"antigravity","extensionName":"antigravity","locale":"en"}}`

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("antigravity: creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if conn.CSRFToken != "" {
		req.Header.Set("X-Codeium-Csrf-Token", conn.CSRFToken)
	}

	c.logger.Debug("fetching Antigravity quotas", "url", endpoint)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Reset connection on error to force re-detection
		c.connection = nil
		return nil, fmt.Errorf("%w: %v", ErrAntigravityConnectionFailed, err)
	}
	defer resp.Body.Close()

	c.logger.Debug("Antigravity quota response received", "status", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		c.connection = nil // Reset connection
		return nil, fmt.Errorf("antigravity: unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16)) // 64KB limit
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrAntigravityInvalidResponse, err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty response body", ErrAntigravityInvalidResponse)
	}

	var quotaResp AntigravityUserStatusResponse
	if err := json.Unmarshal(body, &quotaResp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAntigravityInvalidResponse, err)
	}

	if quotaResp.UserStatus == nil {
		if quotaResp.Message != "" {
			return nil, fmt.Errorf("%w: %s", ErrAntigravityNotAuthenticated, quotaResp.Message)
		}
		return nil, ErrAntigravityNotAuthenticated
	}

	if modelIDs := quotaResp.ActiveModelIDs(); len(modelIDs) > 0 {
		c.logger.Debug("Antigravity quotas fetched successfully",
			"models", len(modelIDs),
			"email", quotaResp.UserStatus.Email,
		)
	}

	return &quotaResp, nil
}

// IsConnected returns true if a valid connection exists.
func (c *AntigravityClient) IsConnected() bool {
	return c.connection != nil
}

// Reset clears the cached connection, forcing re-detection.
func (c *AntigravityClient) Reset() {
	c.connection = nil
}

// detectProcess finds the Antigravity language server process.
func (c *AntigravityClient) detectProcess(ctx context.Context) (*AntigravityProcessInfo, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		return c.detectProcessUnix(ctx)
	case "windows":
		return c.detectProcessWindows(ctx)
	default:
		return nil, fmt.Errorf("antigravity: unsupported platform %s", runtime.GOOS)
	}
}

// detectProcessUnix finds the process on Unix-like systems.
func (c *AntigravityClient) detectProcessUnix(ctx context.Context) (*AntigravityProcessInfo, error) {
	cmd := exec.CommandContext(ctx, "ps", "aux")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("antigravity: ps command failed: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "antigravity") {
			continue
		}

		// Skip installation scripts
		if strings.Contains(lower, "server installation script") {
			continue
		}

		// Look for language server indicators
		hasServerSignal := strings.Contains(line, "language-server") ||
			strings.Contains(line, "lsp") ||
			strings.Contains(line, "--csrf_token") ||
			strings.Contains(line, "--extension_server_port") ||
			strings.Contains(line, "exa.language_server_pb")

		if !hasServerSignal {
			continue
		}

		return c.parseUnixProcessLine(line)
	}

	return nil, ErrAntigravityProcessNotFound
}

// parseUnixProcessLine extracts process info from a ps aux line.
func (c *AntigravityClient) parseUnixProcessLine(line string) (*AntigravityProcessInfo, error) {
	// ps aux format: USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND...
	parts := strings.Fields(line)
	if len(parts) < 11 {
		return nil, ErrAntigravityProcessNotFound
	}

	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, ErrAntigravityProcessNotFound
	}

	commandLine := strings.Join(parts[10:], " ")

	return &AntigravityProcessInfo{
		PID:                 pid,
		CSRFToken:           extractArgument(commandLine, "--csrf_token"),
		ExtensionServerPort: extractPortArgument(commandLine, "--extension_server_port"),
		CommandLine:         commandLine,
	}, nil
}

// detectProcessWindows finds the process on Windows.
func (c *AntigravityClient) detectProcessWindows(ctx context.Context) (*AntigravityProcessInfo, error) {
	// Try WMIC first
	cmd := exec.CommandContext(ctx, "wmic", "process", "where",
		"name like '%antigravity%' or commandline like '%antigravity%'",
		"get", "processid,commandline", "/format:csv")

	output, err := cmd.Output()
	if err == nil {
		if info := c.parseWMICOutput(string(output)); info != nil {
			return info, nil
		}
	}

	// Fallback to PowerShell
	return c.detectProcessWindowsPowerShell(ctx)
}

// parseWMICOutput parses WMIC CSV output.
func (c *AntigravityClient) parseWMICOutput(output string) *AntigravityProcessInfo {
	lines := strings.Split(output, "\n")
	var best *AntigravityProcessInfo
	bestScore := -1

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "CommandLine,ProcessId") {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}

		commandLine := strings.Join(parts[1:len(parts)-1], ",")
		if !strings.Contains(strings.ToLower(commandLine), "antigravity") {
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
		if err != nil {
			continue
		}

		info := &AntigravityProcessInfo{
			PID:                 pid,
			CSRFToken:           extractArgument(commandLine, "--csrf_token"),
			ExtensionServerPort: extractPortArgument(commandLine, "--extension_server_port"),
			CommandLine:         commandLine,
		}

		score := scoreWindowsCandidate(info)
		if score > bestScore {
			best = info
			bestScore = score
		}
	}

	return best
}

// detectProcessWindowsPowerShell uses PowerShell as fallback.
func (c *AntigravityClient) detectProcessWindowsPowerShell(ctx context.Context) (*AntigravityProcessInfo, error) {
	cmd := exec.CommandContext(ctx, "powershell", "-Command",
		"Get-Process | Where-Object { $_.ProcessName -like '*antigravity*' } | Select-Object Id, ProcessName | ConvertTo-Json")

	output, err := cmd.Output()
	if err != nil {
		return nil, ErrAntigravityProcessNotFound
	}

	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, ErrAntigravityProcessNotFound
	}

	var processes []struct {
		Id int `json:"Id"`
	}

	// Handle both single object and array responses
	if err := json.Unmarshal(output, &processes); err != nil {
		var single struct {
			Id int `json:"Id"`
		}
		if err := json.Unmarshal(output, &single); err != nil {
			return nil, ErrAntigravityProcessNotFound
		}
		processes = append(processes, single)
	}

	var best *AntigravityProcessInfo
	bestScore := -1

	for _, proc := range processes {
		cmdLineCmd := exec.CommandContext(ctx, "powershell", "-Command",
			fmt.Sprintf("(Get-CimInstance Win32_Process -Filter 'ProcessId = %d').CommandLine", proc.Id))

		cmdOutput, err := cmdLineCmd.Output()
		if err != nil {
			continue
		}

		commandLine := strings.TrimSpace(string(cmdOutput))
		if !strings.Contains(strings.ToLower(commandLine), "antigravity") {
			continue
		}

		info := &AntigravityProcessInfo{
			PID:                 proc.Id,
			CSRFToken:           extractArgument(commandLine, "--csrf_token"),
			ExtensionServerPort: extractPortArgument(commandLine, "--extension_server_port"),
			CommandLine:         commandLine,
		}

		score := scoreWindowsCandidate(info)
		if score > bestScore {
			best = info
			bestScore = score
		}
	}

	if best == nil {
		return nil, ErrAntigravityProcessNotFound
	}

	return best, nil
}

// discoverPorts finds listening ports for a process.
func (c *AntigravityClient) discoverPorts(ctx context.Context, pid int) ([]int, error) {
	switch runtime.GOOS {
	case "darwin":
		return c.discoverPortsMacOS(ctx, pid)
	case "linux":
		return c.discoverPortsLinux(ctx, pid)
	case "windows":
		return c.discoverPortsWindows(ctx, pid)
	default:
		return nil, fmt.Errorf("antigravity: unsupported platform %s", runtime.GOOS)
	}
}

// discoverPortsMacOS uses lsof to find listening ports.
func (c *AntigravityClient) discoverPortsMacOS(ctx context.Context, pid int) ([]int, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", strconv.Itoa(pid))
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parsePortsFromLsof(string(output)), nil
}

// discoverPortsLinux uses ss or netstat to find listening ports.
func (c *AntigravityClient) discoverPortsLinux(ctx context.Context, pid int) ([]int, error) {
	// Try ss first
	cmd := exec.CommandContext(ctx, "ss", "-tlnp")
	output, err := cmd.Output()
	if err == nil {
		ports := parsePortsFromSS(string(output), pid)
		if len(ports) > 0 {
			return ports, nil
		}
	}

	// Fallback to netstat
	cmd = exec.CommandContext(ctx, "netstat", "-tlnp")
	output, err = cmd.Output()
	if err != nil {
		return nil, err
	}

	return parsePortsFromNetstat(string(output), pid), nil
}

// discoverPortsWindows uses netstat to find listening ports.
func (c *AntigravityClient) discoverPortsWindows(ctx context.Context, pid int) ([]int, error) {
	cmd := exec.CommandContext(ctx, "netstat", "-ano")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parsePortsFromWindowsNetstat(string(output), pid), nil
}

// probeForConnectAPI probes ports to find the Connect RPC endpoint.
func (c *AntigravityClient) probeForConnectAPI(ctx context.Context, ports []int, csrfToken string) (*AntigravityConnection, error) {
	for _, port := range ports {
		// Try HTTPS first (language server typically uses self-signed certs)
		if conn := c.probePort(ctx, port, "https", csrfToken); conn != nil {
			return conn, nil
		}

		// Fallback to HTTP
		if conn := c.probePort(ctx, port, "http", csrfToken); conn != nil {
			return conn, nil
		}
	}

	return nil, ErrAntigravityConnectionFailed
}

// probePort tests if a port has the Connect RPC endpoint.
func (c *AntigravityClient) probePort(ctx context.Context, port int, protocol, csrfToken string) *AntigravityConnection {
	baseURL := fmt.Sprintf("%s://127.0.0.1:%d", protocol, port)
	endpoint := baseURL + "/exa.language_server_pb.LanguageServerService/GetUnleashData"

	reqCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(`{"wrapper_data":{}}`))
	if err != nil {
		return nil
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if csrfToken != "" {
		req.Header.Set("X-Codeium-Csrf-Token", csrfToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	// Valid Connect API returns 200 or 401
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
		return &AntigravityConnection{
			BaseURL:   baseURL,
			CSRFToken: csrfToken,
			Port:      port,
			Protocol:  protocol,
		}
	}

	return nil
}

// Helper functions

func extractArgument(commandLine, argName string) string {
	// Try --arg=value format
	eqPattern := regexp.MustCompile(argName + `=([^\s"']+|"[^"]*"|'[^']*')`)
	if match := eqPattern.FindStringSubmatch(commandLine); len(match) > 1 {
		return strings.Trim(match[1], `"'`)
	}

	// Try --arg value format
	spacePattern := regexp.MustCompile(argName + `\s+([^\s"']+|"[^"]*"|'[^']*')`)
	if match := spacePattern.FindStringSubmatch(commandLine); len(match) > 1 {
		return strings.Trim(match[1], `"'`)
	}

	return ""
}

func extractPortArgument(commandLine, argName string) int {
	portStr := extractArgument(commandLine, argName)
	if portStr == "" {
		return 0
	}
	port, _ := strconv.Atoi(portStr)
	return port
}

func scoreWindowsCandidate(info *AntigravityProcessInfo) int {
	lower := strings.ToLower(info.CommandLine)
	score := 0

	if strings.Contains(lower, "antigravity") {
		score++
	}
	if strings.Contains(lower, "lsp") {
		score += 5
	}
	if info.ExtensionServerPort > 0 {
		score += 10
	}
	if info.CSRFToken != "" {
		score += 20
	}
	if strings.Contains(lower, "language_server") ||
		strings.Contains(lower, "language-server") ||
		strings.Contains(lower, "exa.language_server_pb") {
		score += 50
	}

	return score
}

func parsePortsFromLsof(output string) []int {
	var ports []int
	portPattern := regexp.MustCompile(`:(\d+)\s+\(LISTEN\)`)

	for _, line := range strings.Split(output, "\n") {
		if match := portPattern.FindStringSubmatch(line); len(match) > 1 {
			if port, err := strconv.Atoi(match[1]); err == nil {
				ports = append(ports, port)
			}
		}
	}

	return ports
}

func parsePortsFromSS(output string, pid int) []int {
	var ports []int
	pidPattern := fmt.Sprintf("pid=%d,", pid)
	portPattern := regexp.MustCompile(`:(\d+)\s`)

	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, pidPattern) {
			continue
		}
		if match := portPattern.FindStringSubmatch(line); len(match) > 1 {
			if port, err := strconv.Atoi(match[1]); err == nil {
				ports = append(ports, port)
			}
		}
	}

	return ports
}

func parsePortsFromNetstat(output string, pid int) []int {
	var ports []int
	pidPattern := fmt.Sprintf("%d/", pid)
	portPattern := regexp.MustCompile(`:(\d+)\s`)

	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, pidPattern) {
			continue
		}
		if match := portPattern.FindStringSubmatch(line); len(match) > 1 {
			if port, err := strconv.Atoi(match[1]); err == nil {
				ports = append(ports, port)
			}
		}
	}

	return ports
}

func parsePortsFromWindowsNetstat(output string, pid int) []int {
	var ports []int
	portPattern := regexp.MustCompile(`:(\d+)$`)

	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "LISTENING") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}

		linePID, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil || linePID != pid {
			continue
		}

		localAddr := parts[1]
		if match := portPattern.FindStringSubmatch(localAddr); len(match) > 1 {
			if port, err := strconv.Atoi(match[1]); err == nil {
				ports = append(ports, port)
			}
		}
	}

	return ports
}
