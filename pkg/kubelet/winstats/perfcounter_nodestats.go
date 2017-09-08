// +build windows

/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package winstats

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/lxn/win"
)

// NewPerfCounterClient creates a client using perf counters
func NewPerfCounterClient() (Client, error) {
	return NewClient(&perfCounterNodeStatsClient{})
}

// perfCounterNodeStatsClient is a client that provides Windows Stats via PerfCounters
type perfCounterNodeStatsClient struct {
	nodeStats
	mu sync.RWMutex
}

func (p *perfCounterNodeStatsClient) startMonitoring() error {
	memory, err := getPhysicallyInstalledSystemMemoryBytes()
	if err != nil {
		return err
	}
	p.nodeStats.memoryPhysicalCapacityBytes = memory

	version, err := exec.Command("cmd", "/C", "ver").Output()
	if err != nil {
		return err
	}
	p.kernelVersion = strings.TrimSpace(string(version))

	cpuChan, err := readPerformanceCounter(cpuQuery)
	if err != nil {
		return err
	}

	memWorkingSetChan, err := readPerformanceCounter(memoryPrivWorkingSetQuery)
	if err != nil {
		return err
	}

	memCommittedBytesChan, err := readPerformanceCounter(memoryCommittedBytesQuery)
	if err != nil {
		return err
	}

	go p.startNodeMonitoring(cpuChan, memWorkingSetChan, memCommittedBytesChan)
	return nil
}

func (p *perfCounterNodeStatsClient) getMachineInfo() (*cadvisorapi.MachineInfo, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	return &cadvisorapi.MachineInfo{
		NumCores:       runtime.NumCPU(),
		MemoryCapacity: p.memoryPhysicalCapacityBytes,
		MachineID:      hostname,
	}, nil
}
func (p *perfCounterNodeStatsClient) getVersionInfo() (*cadvisorapi.VersionInfo, error) {
	return &cadvisorapi.VersionInfo{
		KernelVersion: p.kernelVersion,
	}, nil
}

func (p *perfCounterNodeStatsClient) getNodeStats() (nodeStats, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.nodeStats, nil
}

func (p *perfCounterNodeStatsClient) startNodeMonitoring(cpuChan, memWorkingSetChan, memCommittedBytesChan chan metric) {
	for {
		select {
		case cpu := <-cpuChan:
			p.updateCPU(cpu)
		case mWorkingSet := <-memWorkingSetChan:
			p.updateMemoryWorkingSet(mWorkingSet)
		case mCommittedBytes := <-memCommittedBytesChan:
			p.updateMemoryCommittedBytes(mCommittedBytes)
		}
		p.lastUpdatedTime = time.Now()
	}
}
func (p *perfCounterNodeStatsClient) updateCPU(cpu metric) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cpuCores := runtime.NumCPU()
	// This converts perf counter data which is cpu percentage for all cores into nanoseconds.
	// The formula is (cpuPercentage / 100.0) * #cores * 1e+9 (nano seconds). More info here:
	// https://github.com/kubernetes/heapster/issues/650
	newValue := p.cpuUsageCoreNanoSeconds.Value + uint64((float64(cpu.Value)/100.0)*float64(cpuCores)*1000000000)

	p.cpuUsageCoreNanoSeconds = metric{
		Name:      cpu.Name,
		Value:     newValue,
		Timestamp: cpu.Timestamp,
	}
}

func (p *perfCounterNodeStatsClient) updateMemoryWorkingSet(mWorkingSet metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.memoryPrivWorkingSetBytes = mWorkingSet
}

func (p *perfCounterNodeStatsClient) updateMemoryCommittedBytes(mCommittedBytes metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.memoryCommittedBytes = mCommittedBytes
}

func getPhysicallyInstalledSystemMemoryBytes() (uint64, error) {
	var physicalMemoryKiloBytes uint64

	if ok := win.GetPhysicallyInstalledSystemMemory(&physicalMemoryKiloBytes); !ok {
		return 0, errors.New("unable to read physical memory")
	}

	return physicalMemoryKiloBytes * 1024, nil // convert kilobytes to bytes
}
