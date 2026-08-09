package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/sirupsen/logrus"
)

type Usage struct {
	dataStore    sync.Map
	listenAddr   string
	shutdownCtx  context.Context
	cancelFunc   context.CancelFunc
	server       *http.Server
	logger       *logrus.Logger
	sniffer      bool
	snifferLog   string
	mu           sync.Mutex
	fileMu       sync.Mutex
	statsMu      sync.Mutex
	totalTraffic atomic.Uint64
	tunnelStatus atomic.Value
	cachedStats  *SystemStats
	statsAt      time.Time
}

type PortUsage struct {
	Port  int
	Usage uint64
}

type SystemStats struct {
	TunnelStatus   string `json:"tunnelStatus"`
	CPUUsage       string `json:"cpuUsage"`
	RAMUsage       string `json:"ramUsage"`
	DiskUsage      string `json:"diskUsage"`
	SwapUsage      string `json:"swapUsage"`
	NetworkTraffic string `json:"networkTraffic"`
	UploadSpeed    string `json:"uploadSpeed"`
	DownloadSpeed  string `json:"downloadSpeed"`
	TunnelTraffic  string `json:"tunnelTraffic"`
	Sniffer        string `json:"sniffer"`
	AllConnections string `json:"allConnections"`
}

func NewDataStore(listenAddr string, shutdownCtx context.Context, snifferLog string, sniffer bool, logger *logrus.Logger) *Usage {
	ctx, cancel := context.WithCancel(shutdownCtx)
	u := &Usage{
		listenAddr:  listenAddr,
		shutdownCtx: ctx,
		cancelFunc:  cancel,
		logger:      logger,
		sniffer:     sniffer,
		snifferLog:  snifferLog,
		mu:          sync.Mutex{},
	}
	u.tunnelStatus.Store("")
	return u
}

const systemStatsTTL = 5 * time.Second

func (m *Usage) SetTunnelStatus(status string) {
	m.tunnelStatus.Store(status)
}

func (m *Usage) loadTunnelStatus() string {
	status := m.tunnelStatus.Load()
	if status == nil {
		return ""
	}
	return status.(string)
}

func (m *Usage) Monitor() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handleIndex) // handle index
	mux.HandleFunc("/stats", m.statsHandler)
	if m.sniffer {
		mux.HandleFunc("/data", m.handleData) // New route for JSON data
	}
	m.server = &http.Server{
		Addr:    m.listenAddr,
		Handler: mux,
	}

	go func() {
		<-m.shutdownCtx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Attempt to gracefully shut down the server
		if err := m.server.Shutdown(shutdownCtx); err != nil {
			m.logger.Errorf("sniffer server shutdown error: %v", err)
		}
	}()

	// start save data
	if m.sniffer {
		go func() {
			ticker := time.NewTicker(15 * time.Second) // every 5 seconds
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					m.saveUsageData()
				case <-m.shutdownCtx.Done():
					return
				}
			}
		}()
	}
	// Start the server
	m.logger.Info("sniffer service listening on port: ", m.listenAddr)
	if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		m.logger.Errorf("sniffer server error: %v", err)
	}
}

//go:embed index.html
var indexHTML embed.FS

func (m *Usage) handleIndex(w http.ResponseWriter, r *http.Request) {
	usageData := m.getUsageFromFile()
	readableData := m.usageDataWithReadableUsage(usageData)

	tmpl, err := template.ParseFS(indexHTML, "index.html")
	if err != nil {
		m.logger.Errorf("error parsing template: %v", err)
		return
	}

	err = tmpl.Execute(w, readableData)
	if err != nil {
		m.logger.Errorf("error executing template: %v", err)
	}
}

func (m *Usage) handleData(w http.ResponseWriter, r *http.Request) {
	usageData := m.getUsageFromFile()
	readableData := m.usageDataWithReadableUsage(usageData)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(readableData); err != nil {
		m.logger.Errorf("error encoding JSON response: %v", err)
	}
}

func (m *Usage) statsHandler(w http.ResponseWriter, r *http.Request) {
	stats, err := m.getSystemStats()
	if err != nil {
		m.logger.Error("Error fetching system stats:", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		m.logger.Error("Error encoding JSON:", err)
	}
}

func (m *Usage) AddOrUpdatePort(port int, usage uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Retrieve current usage data for the port
	value, ok := m.dataStore.Load(port)
	if ok {
		// Port exists, update usage
		portUsage := value.(PortUsage)
		portUsage.Usage += usage
		m.dataStore.Store(port, portUsage)
	} else {
		// Port does not exist, create new entry
		m.dataStore.Store(port, PortUsage{Port: port, Usage: usage})
	}
}

func (m *Usage) saveUsageData() {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	// Step 1: Load existing usage data from the JSON file
	var existingUsageData []PortUsage
	file, err := os.Open(m.snifferLog)
	if err == nil {
		// If the file exists, decode the JSON data into existingUsageData
		decodeErr := json.NewDecoder(file).Decode(&existingUsageData)
		closeErr := file.Close()
		if decodeErr != nil {
			m.logger.Errorf("error decoding JSON data: %v", decodeErr)
			return
		}
		if closeErr != nil {
			m.logger.Errorf("error closing JSON file: %v", closeErr)
			return
		}
	} else if !os.IsNotExist(err) {
		// Log any error except file not existing
		m.logger.Errorf("error opening JSON file: %v", err)
		return
	}

	// Step 2: Get current usage data from sync.Map
	currentUsageData := m.collectUsageDataFromSyncMap()

	// Step 3: Merge the existing and current usage data into a map to avoid duplicates
	usageMap := make(map[int]PortUsage)

	// Add existing usage data to the map
	for _, usage := range existingUsageData {
		usageMap[usage.Port] = usage
	}

	// Append or update current usage data in the map
	for _, usage := range currentUsageData {
		if existing, exists := usageMap[usage.Port]; exists {
			// Update existing port usage
			existing.Usage += usage.Usage
			usageMap[usage.Port] = existing
		} else {
			// Add new port usage
			usageMap[usage.Port] = usage
		}
	}

	// Step 4: Convert the map back to a slice
	var mergedUsageData []PortUsage
	var totalTraffic uint64
	for _, usage := range usageMap {
		mergedUsageData = append(mergedUsageData, usage)
		totalTraffic += usage.Usage
	}
	m.totalTraffic.Store(totalTraffic)

	// Step 5: Convert merged data to JSON
	data, err := json.MarshalIndent(mergedUsageData, "", "  ")
	if err != nil {
		m.logger.Errorf("error marshalling usage data: %v", err)
		return
	}

	// Step 6: Write JSON data to file
	err = os.WriteFile(m.snifferLog, data, 0644)
	if err != nil {
		m.logger.Errorf("error writing usage data to file: %v", err)
	}
}

func (m *Usage) getUsageFromFile() []PortUsage {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	// Check if the file exists
	if _, err := os.Stat(m.snifferLog); os.IsNotExist(err) {
		// If the file does not exist, create it and write "null"
		file, err := os.OpenFile(m.snifferLog, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			m.logger.Errorf("error creating file: %v", err)
			return nil
		}

		// Write "null" to the new file
		if _, err := file.Write([]byte("null")); err != nil {
			m.logger.Errorf("error writing 'null' to the file: %v", err)
			file.Close()
			return nil
		}
		if err := file.Close(); err != nil {
			m.logger.Errorf("error closing JSON file: %v", err)
		}

		return nil
	}

	var usageData []PortUsage

	// Open the JSON file
	file, err := os.Open(m.snifferLog)
	if err != nil {
		m.logger.Errorf("error opening JSON file: %v", err)
		return nil
	}
	defer file.Close()

	// Decode the JSON file into the usageData slice
	err = json.NewDecoder(file).Decode(&usageData)
	if err != nil {
		m.logger.Errorf("error decoding JSON data: %v", err)
		return nil
	}

	// Sort usageData by Port in ascending order
	sort.Slice(usageData, func(i, j int) bool {
		return usageData[i].Port < usageData[j].Port
	})

	return usageData
}

// converts the byte usage to a human-readable format
func (m *Usage) usageDataWithReadableUsage(usageData []PortUsage) []struct {
	Port          int
	ReadableUsage string
} {
	var result []struct {
		Port          int
		ReadableUsage string
	}

	for _, portUsage := range usageData {
		result = append(result, struct {
			Port          int
			ReadableUsage string
		}{
			Port:          portUsage.Port,
			ReadableUsage: m.convertBytesToReadable(portUsage.Usage),
		})
	}

	return result
}

// collectUsageDataFromSyncMap gathers data from sync.Map
func (m *Usage) collectUsageDataFromSyncMap() []PortUsage {
	m.mu.Lock()
	defer m.mu.Unlock()

	var usageData []PortUsage
	m.dataStore.Range(func(key, value interface{}) bool {
		if portUsage, ok := value.(PortUsage); ok {
			usageData = append(usageData, portUsage)
			m.dataStore.Delete(key)
		}
		return true
	})
	return usageData
}

// ConvertBytesToReadable converts bytes into a human-readable format (KB, MB, GB)
func (m *Usage) convertBytesToReadable(bytes uint64) string {
	const (
		KB = 1 << (10 * 1) // 1024 bytes
		MB = 1 << (10 * 2) // 1024 KB
		GB = 1 << (10 * 3) // 1024 MB
		TB = 1 << (10 * 4) // 1024 TB
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes) // Bytes
	}
}

func (m *Usage) getSystemStats() (*SystemStats, error) {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	if m.cachedStats != nil && time.Since(m.statsAt) < systemStatsTTL {
		return m.statsSnapshot(m.cachedStats), nil
	}
	stats, err := m.collectSystemStats()
	if err != nil {
		return nil, err
	}
	m.cachedStats = stats
	m.statsAt = time.Now()
	return m.statsSnapshot(stats), nil
}

func (m *Usage) statsSnapshot(stats *SystemStats) *SystemStats {
	copy := *stats
	copy.TunnelStatus = m.loadTunnelStatus()
	copy.TunnelTraffic = m.convertBytesToReadable(m.totalTraffic.Load())
	return &copy
}

func (m *Usage) collectSystemStats() (*SystemStats, error) {

	// Get initial network stats
	initialStats, err := m.getNetworkStats()
	if err != nil {
		return nil, err
	}

	// Wait for 1 second
	time.Sleep(1 * time.Second)

	// Get updated network stats
	finalStats, err := m.getNetworkStats()
	if err != nil {
		return nil, err
	}

	// Get CPU usage
	cpuPercent, err := cpu.Percent(0, false)
	if err != nil {
		return nil, err
	}
	if len(cpuPercent) == 0 {
		return nil, fmt.Errorf("no CPU usage samples found")
	}

	// Get RAM usage
	memStats, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}

	// Get Disk usage
	diskStats, err := disk.Usage("/")
	if err != nil {
		return nil, err
	}

	// Get Swap usage
	swapStats, err := mem.SwapMemory()
	if err != nil {
		return nil, err
	}

	// Get Network traffic
	netStats, err := net.IOCounters(false)
	if err != nil {
		return nil, err
	}
	if len(netStats) == 0 {
		return nil, fmt.Errorf("no network IO counters found")
	}

	// Get all active network connections (TCP, UDP, etc.)
	connections, err := net.Connections("all")
	if err != nil {
		return nil, err
	}

	// Calculate upload and download speeds
	uploadSpeed := float64(counterDelta(finalStats.BytesSent, initialStats.BytesSent))
	downloadSpeed := float64(counterDelta(finalStats.BytesRecv, initialStats.BytesRecv))

	stats := &SystemStats{
		TunnelStatus:   m.loadTunnelStatus(),
		CPUUsage:       m.formatFloat(cpuPercent[0]),
		RAMUsage:       m.convertBytesToReadable(memStats.Used),
		DiskUsage:      m.convertBytesToReadable(diskStats.Used),
		SwapUsage:      m.convertBytesToReadable(swapStats.Used),
		NetworkTraffic: m.convertBytesToReadable(netStats[0].BytesSent + netStats[0].BytesRecv),
		DownloadSpeed:  m.formatSpeed(downloadSpeed),
		UploadSpeed:    m.formatSpeed(uploadSpeed),
		TunnelTraffic:  m.convertBytesToReadable(m.totalTraffic.Load()),
		Sniffer:        map[bool]string{true: "Running", false: "Not running"}[m.sniffer],
		AllConnections: fmt.Sprintf("%d", len(connections)),
	}

	return stats, nil
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func (m *Usage) formatSpeed(bytesPerSec float64) string {
	if bytesPerSec >= 1e9 {
		return fmt.Sprintf("%.2f GB/s", bytesPerSec/1e9)
	} else if bytesPerSec >= 1e6 {
		return fmt.Sprintf("%.2f MB/s", bytesPerSec/1e6)
	} else if bytesPerSec >= 1e3 {
		return fmt.Sprintf("%.2f KB/s", bytesPerSec/1e3)
	}
	return fmt.Sprintf("%.2f B/s", bytesPerSec)
}

func (m *Usage) formatFloat(value float64) string {
	return fmt.Sprintf("%.2f%%", value)
}

func (m *Usage) getNetworkStats() (*net.IOCountersStat, error) {
	ioCounters, err := net.IOCounters(false)
	if err != nil {
		return nil, err
	}
	if len(ioCounters) == 0 {
		return nil, fmt.Errorf("no network IO counters found")
	}
	return &ioCounters[0], nil
}
