package database

import (
	"testing"
	"time"
)

func TestServerResourceSamplesPreserveInactiveAndNullableValues(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	cpu := 37.5
	memory := uint64(512 << 20)
	limit := uint64(2 << 30)
	samples := []*ServerResourceSample{
		{
			ServerName: "sample-server", Timestamp: now.Add(-time.Minute), Running: true,
			CPUPercent: &cpu, MemoryCurrentBytes: &memory, MemoryLimitBytes: &limit,
		},
		{
			ServerName: "sample-server", Timestamp: now, Running: false,
			MemoryPeakBytes: &limit,
		},
	}
	for _, sample := range samples {
		if err := store.CreateServerResourceSample(sample); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListServerResourceSamples("sample-server", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("samples=%d, want 2", len(got))
	}
	if !got[0].Running || got[0].CPUPercent == nil || *got[0].CPUPercent != cpu {
		t.Fatalf("running sample was not preserved: %#v", got[0])
	}
	if got[1].Running || got[1].CPUPercent != nil || got[1].MemoryCurrentBytes != nil {
		t.Fatalf("inactive nullable sample was not preserved: %#v", got[1])
	}
	if got[1].MemoryPeakBytes == nil || *got[1].MemoryPeakBytes != limit {
		t.Fatalf("inactive peak was not preserved: %#v", got[1])
	}
}

func TestServerResourceSamplesAreDownsampled(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for index := 0; index < 20; index++ {
		cpu := float64(index)
		memory := uint64(index + 1)
		sample := &ServerResourceSample{
			ServerName: "downsample-server", Timestamp: now.Add(time.Duration(index) * time.Minute),
			Running: true, CPUPercent: &cpu, MemoryCurrentBytes: &memory,
		}
		if err := store.CreateServerResourceSample(sample); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListServerResourceSamples("downsample-server", now.Add(-time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("samples=%d, want 5", len(got))
	}
	if !got[0].Timestamp.Before(got[len(got)-1].Timestamp) {
		t.Fatal("downsampled history is not chronological")
	}
	for _, sample := range got {
		if sample.CPUPercent == nil || sample.MemoryCurrentBytes == nil {
			t.Fatalf("downsample lost running values: %#v", sample)
		}
	}
}
