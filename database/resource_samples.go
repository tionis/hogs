package database

import (
	"database/sql"
	"fmt"
	"time"
)

type ServerResourceSample struct {
	ID                 int       `json:"id"`
	ServerID           int       `json:"serverId"`
	Timestamp          time.Time `json:"timestamp"`
	Running            bool      `json:"running"`
	CPUPercent         *float64  `json:"cpuPercent,omitempty"`
	CPULimitPercent    *float64  `json:"cpuLimitPercent,omitempty"`
	MemoryCurrentBytes *uint64   `json:"memoryCurrentBytes,omitempty"`
	MemoryPeakBytes    *uint64   `json:"memoryPeakBytes,omitempty"`
	MemoryHighBytes    *uint64   `json:"memoryHighBytes,omitempty"`
	MemoryLimitBytes   *uint64   `json:"memoryLimitBytes,omitempty"`
}

func (s *Store) CreateServerResourceSample(sample *ServerResourceSample) error {
	running := 0
	if sample.Running {
		running = 1
	}
	result, err := s.DB.Exec(`INSERT INTO server_resource_samples (
		server_id, timestamp, running, cpu_percent, cpu_limit_percent,
		memory_current_bytes, memory_peak_bytes, memory_high_bytes, memory_limit_bytes
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sample.ServerID, sample.Timestamp.UTC().Format(time.RFC3339Nano), running,
		sample.CPUPercent, sample.CPULimitPercent, sample.MemoryCurrentBytes,
		sample.MemoryPeakBytes, sample.MemoryHighBytes, sample.MemoryLimitBytes,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	sample.ID = int(id)
	return nil
}

func (s *Store) ListServerResourceSamples(serverID int, since time.Time, maxPoints int) ([]ServerResourceSample, error) {
	if maxPoints <= 0 {
		maxPoints = 800
	}
	rows, err := s.DB.Query(`SELECT id, server_id, timestamp, running, cpu_percent,
		cpu_limit_percent, memory_current_bytes, memory_peak_bytes, memory_high_bytes,
		memory_limit_bytes
		FROM server_resource_samples
		WHERE server_id = ? AND timestamp >= ?
		ORDER BY timestamp ASC`,
		serverID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	samples := make([]ServerResourceSample, 0)
	for rows.Next() {
		var sample ServerResourceSample
		var timestamp string
		var running int
		var cpu, cpuLimit sql.NullFloat64
		var memoryCurrent, memoryPeak, memoryHigh, memoryLimit sql.NullInt64
		if err := rows.Scan(
			&sample.ID, &sample.ServerID, &timestamp, &running, &cpu, &cpuLimit,
			&memoryCurrent, &memoryPeak, &memoryHigh, &memoryLimit,
		); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse resource sample timestamp: %w", err)
		}
		sample.Timestamp = parsed
		sample.Running = running == 1
		sample.CPUPercent = nullableFloat(cpu)
		sample.CPULimitPercent = nullableFloat(cpuLimit)
		sample.MemoryCurrentBytes = nullableUint(memoryCurrent)
		sample.MemoryPeakBytes = nullableUint(memoryPeak)
		sample.MemoryHighBytes = nullableUint(memoryHigh)
		sample.MemoryLimitBytes = nullableUint(memoryLimit)
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return downsampleResourceSamples(samples, maxPoints), nil
}

func (s *Store) CleanupServerResourceSamples(retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	_, err := s.DB.Exec("DELETE FROM server_resource_samples WHERE timestamp < ?", cutoff)
	return err
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func nullableUint(value sql.NullInt64) *uint64 {
	if !value.Valid || value.Int64 < 0 {
		return nil
	}
	result := uint64(value.Int64)
	return &result
}

func downsampleResourceSamples(samples []ServerResourceSample, maxPoints int) []ServerResourceSample {
	if maxPoints <= 0 || len(samples) <= maxPoints {
		return samples
	}
	result := make([]ServerResourceSample, 0, maxPoints)
	for start := 0; start < len(samples); {
		end := (len(samples) * (len(result) + 1)) / maxPoints
		if end <= start {
			end = start + 1
		}
		if end > len(samples) {
			end = len(samples)
		}
		result = append(result, aggregateResourceSamples(samples[start:end]))
		start = end
	}
	return result
}

func aggregateResourceSamples(samples []ServerResourceSample) ServerResourceSample {
	result := samples[len(samples)-1]
	result.ID = 0
	var cpuTotal float64
	var cpuCount int
	var memoryTotal uint64
	var memoryCount uint64
	var peak uint64
	var hasPeak bool
	result.Running = false
	for _, sample := range samples {
		result.Running = result.Running || sample.Running
		if sample.CPUPercent != nil {
			cpuTotal += *sample.CPUPercent
			cpuCount++
		}
		if sample.MemoryCurrentBytes != nil {
			memoryTotal += *sample.MemoryCurrentBytes
			memoryCount++
		}
		if sample.MemoryPeakBytes != nil && (!hasPeak || *sample.MemoryPeakBytes > peak) {
			peak = *sample.MemoryPeakBytes
			hasPeak = true
		}
	}
	result.CPUPercent = nil
	if cpuCount > 0 {
		average := cpuTotal / float64(cpuCount)
		result.CPUPercent = &average
	}
	result.MemoryCurrentBytes = nil
	if memoryCount > 0 {
		average := memoryTotal / memoryCount
		result.MemoryCurrentBytes = &average
	}
	result.MemoryPeakBytes = nil
	if hasPeak {
		result.MemoryPeakBytes = &peak
	}
	result.CPULimitPercent = nil
	result.MemoryHighBytes = nil
	result.MemoryLimitBytes = nil
	for index := len(samples) - 1; index >= 0; index-- {
		sample := samples[index]
		if result.CPULimitPercent == nil && sample.CPULimitPercent != nil {
			result.CPULimitPercent = sample.CPULimitPercent
		}
		if result.MemoryHighBytes == nil && sample.MemoryHighBytes != nil {
			result.MemoryHighBytes = sample.MemoryHighBytes
		}
		if result.MemoryLimitBytes == nil && sample.MemoryLimitBytes != nil {
			result.MemoryLimitBytes = sample.MemoryLimitBytes
		}
	}
	return result
}
