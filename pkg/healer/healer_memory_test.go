package healer

import (
	"testing"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/util"
)

func TestHealer_SanitizeReferences_ClearsAllMaps(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Populate all tracking maps
	healer.healedPodsMu.Lock()
	healer.HealedPods["ns/pod1"] = time.Now()
	healer.healedPodsMu.Unlock()
	healer.healedNodesMu.Lock()
	healer.HealedNodes["node1"] = time.Now()
	healer.healedNodesMu.Unlock()
	healer.healedVMsMu.Lock()
	healer.HealedVMs["ns/vm1"] = time.Now()
	healer.healedVMsMu.Unlock()
	healer.healedCRDsMu.Lock()
	healer.HealedCRDs["gvr/ns/name"] = time.Now()
	healer.healedCRDsMu.Unlock()
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs["resource/ns/name"] = true
	healer.trackedCRDsMu.Unlock()
	healer.optimizedPodsMu.Lock()
	healer.OptimizedPods["ns/pod2"] = time.Now()
	healer.optimizedPodsMu.Unlock()
	healer.currentClusterStrainMu.Lock()
	healer.CurrentClusterStrain = &util.ClusterStrainInfo{HasStrain: true}
	healer.currentClusterStrainMu.Unlock()

	healer.SanitizeReferences()

	// All maps and strain should be empty/nil
	healer.healedPodsMu.RLock()
	podsLen := len(healer.HealedPods)
	healer.healedPodsMu.RUnlock()
	healer.healedNodesMu.RLock()
	nodesLen := len(healer.HealedNodes)
	healer.healedNodesMu.RUnlock()
	healer.healedVMsMu.RLock()
	vmsLen := len(healer.HealedVMs)
	healer.healedVMsMu.RUnlock()
	healer.healedCRDsMu.RLock()
	crdsLen := len(healer.HealedCRDs)
	healer.healedCRDsMu.RUnlock()
	healer.trackedCRDsMu.RLock()
	trackedLen := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.RUnlock()
	healer.optimizedPodsMu.RLock()
	optLen := len(healer.OptimizedPods)
	healer.optimizedPodsMu.RUnlock()
	healer.currentClusterStrainMu.RLock()
	strainNil := healer.CurrentClusterStrain == nil
	healer.currentClusterStrainMu.RUnlock()

	if podsLen != 0 || nodesLen != 0 || vmsLen != 0 || crdsLen != 0 || trackedLen != 0 || optLen != 0 || !strainNil {
		t.Errorf("SanitizeReferences() did not clear all state: HealedPods=%d HealedNodes=%d HealedVMs=%d HealedCRDs=%d TrackedCRDs=%d OptimizedPods=%d CurrentClusterStrain=nil=%v",
			podsLen, nodesLen, vmsLen, crdsLen, trackedLen, optLen, strainNil)
	}
}

func TestHealer_CheckMemory_OverLimit_SanitizesAndRequestsRestart(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Inject heap over limit (100 MB), limit 50 MB
	healer.MemoryLimitMB = 50
	healer.RestartOnMemoryLimit = true
	healer.RestartRequested = make(chan struct{})
	healer.MemoryReadFunc = func() uint64 { return 100 * 1024 * 1024 }

	// Add some state so we can verify sanitization
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs["r/ns/n"] = true
	healer.trackedCRDsMu.Unlock()

	healer.checkMemory()

	// Maps should be cleared by SanitizeReferences
	healer.trackedCRDsMu.RLock()
	lenTracked := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.RUnlock()
	if lenTracked != 0 {
		t.Errorf("Expected TrackedCRDs to be cleared after checkMemory over limit, got len %d", lenTracked)
	}
	// Restart should have been requested
	if !healer.IsRestartRequested() {
		t.Error("Expected IsRestartRequested() to be true after checkMemory over limit with RestartOnMemoryLimit=true")
	}
	// RestartRequested channel should be closed (receive should not block)
	select {
	case <-healer.RestartRequested:
		// closed, good
	default:
		t.Error("Expected RestartRequested channel to be closed")
	}
}

func TestHealer_CheckMemory_UnderLimit_NoAction(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	healer.MemoryLimitMB = 50
	healer.RestartRequested = make(chan struct{})
	healer.MemoryReadFunc = func() uint64 { return 10 * 1024 * 1024 } // 10 MB, under limit

	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs["r/ns/n"] = true
	healer.trackedCRDsMu.Unlock()

	healer.checkMemory()

	// State should be unchanged
	healer.trackedCRDsMu.RLock()
	lenTracked := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.RUnlock()
	if lenTracked != 1 {
		t.Errorf("Expected TrackedCRDs to be unchanged (len 1), got %d", lenTracked)
	}
	if healer.IsRestartRequested() {
		t.Error("Expected IsRestartRequested() to be false when under limit")
	}
	select {
	case <-healer.RestartRequested:
		t.Error("RestartRequested should not be closed when under limit")
	default:
		// not closed, good
	}
}

func TestHealer_CheckMemory_DoesNothingWhenDisabled(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	healer.MemoryLimitMB = 0
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs["r/ns/n"] = true
	healer.trackedCRDsMu.Unlock()

	healer.checkMemory()

	// State unchanged (checkMemory returns early when MemoryLimitMB == 0)
	healer.trackedCRDsMu.RLock()
	lenTracked := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.RUnlock()
	if lenTracked != 1 {
		t.Errorf("Expected TrackedCRDs unchanged when memory limit disabled, got len %d", lenTracked)
	}
}
