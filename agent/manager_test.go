package agent

import (
	"testing"
	"time"
)

func TestServerResourcesRejectsStaleObservations(t *testing.T) {
	manager := &Manager{resources: map[string]ResourceStatus{
		"fresh": {SampledAt: time.Now().Add(-5 * time.Second)},
		"stale": {SampledAt: time.Now().Add(-time.Minute)},
	}}

	if _, found := manager.ServerResources("fresh"); !found {
		t.Fatal("fresh resource observation was rejected")
	}
	if _, found := manager.ServerResources("stale"); found {
		t.Fatal("stale resource observation was accepted")
	}
	if _, found := manager.ServerResources("missing"); found {
		t.Fatal("missing resource observation was accepted")
	}
}
