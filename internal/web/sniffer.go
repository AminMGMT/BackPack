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
	dataStore   sync.Map
	listenAddr  string
	shutdownCtx context.Context
	cancelFunc  context.CancelFunc
	server      *http.Server
	logger      *logrus.Logger
	sniffer     bool
	snifferLog  string
	// mu guards the per-port accounting only. AddOrUpdatePort runs once per
	// read on every forwarded connection, so nothing slow may ever be done
	// while holding it — the file writing and the stat collection below have
	// their own locks for exactly that reason.
	mu sync.Mutex
	// Written by the save loop, read by the stats endpoint: two goroutines, no
	// lock between them, on a plain uint64. Atomic is the cheap fix and does
	// not put disk work behind the counter's lock.
	totalTraffic atomic.Uint64
	// fileMu serialises the usage file. Saving reads the file, merges it with
	// what is in memory and writes it back; the save runs on a timer in its
	// own goroutine and the dashboard reads the same file whenever anyone
	// loads the page, so two of them could interleave a read-modify-write and
	// lose whichever finished first.
	fileMu sync.Mutex
	// A getter rather than a *string: the transport rewrites its status from
	// one goroutine while this one reads it, and a pointer hands out no way to
	// synchronise the two. See transport.tunnelStatus.
	tunnelStatus func() string

	stats statsCache
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

func NewDataStore(listenAddr string, shutdownCtx context.Context, snifferLog string, sniffer bool, tunnelStatus func() string, logger *logrus.Logger) *Usage {
	ctx, cancel := context.WithCancel(shutdownCtx)
	u := &Usage{
		listenAddr:   monitorListenAddr(listenAddr),
		shutdownCtx:  ctx,
		cancelFunc:   cancel,
		logger:       logger,
		sniffer:      sniffer,
		snifferLog:   snifferLog,
		tunnelStatus: tunnelStatus,
	}
	return u
}

func (m *Usage) Monitor() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handleIndex) // handle index
	mux.HandleFunc("/stats", m.statsHandler)
	if m.sniffer {
		mux.HandleFunc("/data", m.handleData) // New route for JSON data
	}
	m.server = newMonitorServer(m.listenAddr, monitorHTTP(mux))

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
					go m.saveUsageData()
				case <-m.shutdownCtx.Done():
					return
				}
			}
		}()
	}
	// Start the server
	if monitorIsPublic(m.listenAddr) {
		m.logger.Warnf("the monitor page on %s has no authentication and is reachable from the network; set web_bind = \"127.0.0.1\" and reach it over SSH instead", m.listenAddr)
	}
	m.logger.Info("sniffer service listening on: ", m.listenAddr)
	if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		m.logger.Errorf("sniffer server error: %v", err)
	}
}

//go:embed index.html
var indexHTML embed.FS

func (m *Usage) handleIndex(w http.ResponseWriter, r *http.Request) {
	// "/" is a catch-all in http.ServeMux, so without this every unknown path
	// renders the dashboard instead of saying it does not exist.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	usageData := m.getUsageFromFile()
	readableData := m.usageDataWithReadableUsage(usageData)

	tmpl, err := template.ParseFS(indexHTML, "index.html")
	if err != nil {
		m.logger.Errorf("error parsing template: %v", err)
		http.Error(w, "could not render the monitor page", http.StatusInternalServerError)
		return
	}

	// Nothing useful can be said once Execute has started writing — the status
	// line is long gone by then — so this one is logged and left.
	if err := tmpl.Execute(w, readableData); err != nil {
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
		http.Error(w, "could not collect system stats", http.StatusInternalServerError)
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
	// The whole read-merge-write is one operation on the file. It runs on a
	// timer in its own goroutine, and a save that takes longer than the tick
	// used to be joined by the next one: two of them reading the same file,
	// merging against it and writing back, with whichever finished last
	// silently discarding the other's totals.
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	// Step 1: Load existing usage data from the JSON file
	var existingUsageData []PortUsage
	file, err := os.Open(m.snifferLog)
	if err == nil {
		// If the file exists, decode the JSON data into existingUsageData
		defer file.Close()
		err = json.NewDecoder(file).Decode(&existingUsageData)
		if err != nil {
			m.logger.Errorf("error decoding JSON data: %v", err)
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
	var total uint64
	for _, usage := range usageMap {
		mergedUsageData = append(mergedUsageData, usage)
		total += usage.Usage
	}
	// Published once, at the end. Zeroing the counter and adding the ports
	// back one at a time meant the stats endpoint could read a total that was
	// part-way through being rebuilt — and, on the tick that reset it, read
	// zero and report the tunnel as having carried nothing.
	m.totalTraffic.Store(total)

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
	// Reading shares the lock with saving, so a page load cannot catch the
	// file part-way through being rewritten.
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
		// Closed on the way out however this branch ends; the success path
		// used to return without closing it at all.
		defer file.Close()

		// Write "null" to the new file
		if _, err := file.Write([]byte("null")); err != nil {
			m.logger.Errorf("error writing 'null' to the file: %v", err)
			return nil
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

// getSystemStats returns the host's statistics, sharing one collection between
// everything asking at the same time. The tunnel's own figures are filled in
// afterwards, so they are current even when the host figures came from the
// snapshot a second ago.
func (m *Usage) getSystemStats() (*SystemStats, error) {
	collected, err := m.stats.collect(m.collectSystemStats)
	if err != nil {
		return nil, err
	}

	live := *collected
	live.TunnelStatus = m.status()
	live.TunnelTraffic = m.convertBytesToReadable(m.totalTraffic.Load())
	return &live, nil
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

	// Get CPU usage. The slice is empty rather than an error when the kernel
	// has no sample to give — briefly at boot, and on a host where the counter
	// has not moved — and indexing it then took the whole process down over a
	// number on a status page.
	cpuPercent, err := cpu.Percent(0, false)
	if err != nil {
		return nil, err
	}
	cpuUsage := 0.0
	if len(cpuPercent) > 0 {
		cpuUsage = cpuPercent[0]
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
	var totalNetwork uint64
	if len(netStats) > 0 {
		totalNetwork = netStats[0].BytesSent + netStats[0].BytesRecv
	}

	// Count sockets without materialising every connection record on Linux.
	connectionCount, err := socketCount()
	if err != nil {
		return nil, err
	}

	// Calculate upload and download speeds
	uploadSpeed := float64(finalStats.BytesSent - initialStats.BytesSent)
	downloadSpeed := float64(finalStats.BytesRecv - initialStats.BytesRecv)

	stats := &SystemStats{
		// TunnelStatus and TunnelTraffic are refreshed by getSystemStats after
		// this returns; they are cheap and want to be live, and caching them
		// alongside the host figures would leave the page a couple of seconds
		// behind on the two numbers that describe the tunnel itself.
		TunnelStatus:   m.status(),
		CPUUsage:       m.formatFloat(cpuUsage),
		RAMUsage:       m.convertBytesToReadable(memStats.Used),
		DiskUsage:      m.convertBytesToReadable(diskStats.Used),
		SwapUsage:      m.convertBytesToReadable(swapStats.Used),
		NetworkTraffic: m.convertBytesToReadable(totalNetwork),
		DownloadSpeed:  m.formatSpeed(downloadSpeed),
		UploadSpeed:    m.formatSpeed(uploadSpeed),
		TunnelTraffic:  m.convertBytesToReadable(m.totalTraffic.Load()),
		Sniffer:        map[bool]string{true: "Running", false: "Not running"}[m.sniffer],
		AllConnections: fmt.Sprintf("%d", connectionCount),
	}

	return stats, nil
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

// status reads the transport's current status, tolerating a nil getter so a
// data store built without one still serves.
func (m *Usage) status() string {
	if m.tunnelStatus == nil {
		return ""
	}
	return m.tunnelStatus()
}
