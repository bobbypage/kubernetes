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
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	cadvisorapi "github.com/google/cadvisor/info/v1"
)

// PerfCounterNodeStatsClient is a client that provides Windows Stats via PerfCounters
type PerfCounterNodeStatsClient struct {
	nodeStats
	mu sync.Mutex
}

// NewPerfCounterNodeStatsClient creates a new client
func NewPerfCounterNodeStatsClient() *PerfCounterNodeStatsClient {
	return &PerfCounterNodeStatsClient{}
}

func (p *PerfCounterNodeStatsClient) startMonitoring() error {
	memory, err := getPhysicallyInstalledSystemMemoryBytes()
	p.nodeStats.memoryPhysicalCapacityBytes = memory

	version, err := exec.Command("cmd", "/C", "ver").Output()
	if err != nil {
		return err
	}
	p.kernelVersion = strings.TrimSpace(string(version))

	errChan := make(chan error, 1)
	go p.startNodeMonitoring(errChan)
	err = <-errChan
	return err
}

func (p *PerfCounterNodeStatsClient) getMachineInfo() (*cadvisorapi.MachineInfo, error) {
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
func (p *PerfCounterNodeStatsClient) getVersionInfo() (*cadvisorapi.VersionInfo, error) {
	return &cadvisorapi.VersionInfo{
		KernelVersion: p.kernelVersion,
	}, nil
}

func (p *PerfCounterNodeStatsClient) getNodeStats() (*nodeStats, error) {
	return &p.nodeStats, nil
}
func (p *PerfCounterNodeStatsClient) startNodeMonitoring(errChan chan error) {
	cpuChan, err := readPerformanceCounter(cpuQuery)

	if err != nil {
		errChan <- err
		return
	}

	memWorkingSetChan, err := readPerformanceCounter(memoryPrivWorkingSetQuery)

	if err != nil {
		errChan <- err
		return
	}

	memCommittedBytesChan, err := readPerformanceCounter(memoryCommittedBytesQuery)

	if err != nil {
		errChan <- err
		return
	}

	// no error, send nil over channel
	errChan <- nil

	for {
		select {
		case cpu := <-cpuChan:
			p.updateCPU(cpu)
		case mWorkingSet := <-memWorkingSetChan:
			p.updateMemoryWorkingSet(mWorkingSet)
		case mCommittedBytes := <-memCommittedBytesChan:
			p.updateMemoryCommittedBytes(mCommittedBytes)
		}
	}
}
func (p *PerfCounterNodeStatsClient) updateCPU(cpu metric) {
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

func (p *PerfCounterNodeStatsClient) updateMemoryWorkingSet(mWorkingSet metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.memoryPrivWorkingSetBytes = mWorkingSet
}

func (p *PerfCounterNodeStatsClient) updateMemoryCommittedBytes(mCommittedBytes metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.memoryCommittedBytes = mCommittedBytes
}
