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

// Package winstats provides a client to get node and pod level stats on windows
package winstats

import (
	"context"
	dockerapi "github.com/docker/engine-api/client"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"os"
	"runtime"
	"sync"
	"time"
)

// Client is an object that is used to get stats information.
type Client struct {
	dockerClient                *dockerapi.Client
	cpuUsageCoreNanoSeconds     uint64
	memoryPrivWorkingSetBytes   uint64
	memoryCommittedBytes        uint64
	mu                          sync.Mutex
	memoryPhysicalCapacityBytes uint64
}

// NewClient constructs a Client.
func NewClient() (*Client, error) {
	client := new(Client)

	dockerClient, _ := dockerapi.NewEnvClient()
	client.dockerClient = dockerClient

	// create physical memory
	memory, err := getPhysicallyInstalledSystemMemoryBytes()

	if err != nil {
		return nil, err
	}

	client.memoryPhysicalCapacityBytes = memory

	// start node monitoring (reading perf counters)
	errChan := make(chan error, 1)
	go client.startNodeMonitoring(errChan)

	err = <-errChan
	return client, err
}

// startNodeMonitoring starts reading perf counters of the node and updates
// the client struct with cpu and memory stats
func (c *Client) startNodeMonitoring(errChan chan error) {
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
			c.updateCPU(cpu)
		case mWorkingSet := <-memWorkingSetChan:
			c.updateMemoryWorkingSet(mWorkingSet)
		case mCommittedBytes := <-memCommittedBytesChan:
			c.updateMemoryCommittedBytes(mCommittedBytes)
		}
	}
}

func (c *Client) updateCPU(cpu Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cpuCores := runtime.NumCPU()
	// This converts perf counter data which is cpu percentage for all cores into nanoseconds.
	// The formula is (cpuPercentage / 100.0) * #cores * 1e+9 (nano seconds). More info here:
	// https://github.com/kubernetes/heapster/issues/650
	c.cpuUsageCoreNanoSeconds += uint64((cpu.Value / 100.0) * float64(cpuCores) * 1000000000)
}

func (c *Client) updateMemoryWorkingSet(mWorkingSet Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.memoryPrivWorkingSetBytes = uint64(mWorkingSet.Value)
}

func (c *Client) updateMemoryCommittedBytes(mCommittedBytes Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.memoryCommittedBytes = uint64(mCommittedBytes.Value)
}

// WinContainerInfos returns a map of container infos. The map contains node and
// pod level stats. Analogous to cadvisor GetContainerInfoV2 method.
func (c *Client) WinContainerInfos() (map[string]cadvisorapiv2.ContainerInfo, error) {
	infos := make(map[string]cadvisorapiv2.ContainerInfo)

	// root (node) container
	infos["/"] = *c.createRootContainerInfo()

	return infos, nil
}

// WinMachineInfo returns a cadvisorapi.MachineInfo with details about the
// node machine. Analogous to cadvisor MachineInfo method.
func (c *Client) WinMachineInfo() (*cadvisorapi.MachineInfo, error) {
	hostname, err := os.Hostname()

	if err != nil {
		return nil, err
	}

	return &cadvisorapi.MachineInfo{
		NumCores:       runtime.NumCPU(),
		MemoryCapacity: c.memoryPhysicalCapacityBytes,
		MachineID:      hostname,
	}, nil
}

// WinVersionInfo returns a  cadvisorapi.VersionInfo with version info of
// the kernel and docker runtime. Analogous to cadvisor VersionInfo method.
func (c *Client) WinVersionInfo() (*cadvisorapi.VersionInfo, error) {
	dockerServerVersion, err := c.dockerClient.ServerVersion(context.Background())

	if err != nil {
		return nil, err
	}

	return &cadvisorapi.VersionInfo{
		KernelVersion:    dockerServerVersion.KernelVersion,
		DockerVersion:    dockerServerVersion.Version,
		DockerAPIVersion: dockerServerVersion.APIVersion,
	}, nil
}

func (c *Client) createRootContainerInfo() *cadvisorapiv2.ContainerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	var stats []*cadvisorapiv2.ContainerStats

	stats = append(stats, &cadvisorapiv2.ContainerStats{
		Timestamp: time.Now(),
		Cpu: &cadvisorapi.CpuStats{
			Usage: cadvisorapi.CpuUsage{
				Total: c.cpuUsageCoreNanoSeconds,
			},
		},
		Memory: &cadvisorapi.MemoryStats{
			WorkingSet: c.memoryPrivWorkingSetBytes,
			Usage:      c.memoryCommittedBytes,
		},
	})

	rootInfo := cadvisorapiv2.ContainerInfo{
		Spec: cadvisorapiv2.ContainerSpec{
			HasCpu:    true,
			HasMemory: true,
			Memory: cadvisorapiv2.MemorySpec{
				Limit: c.memoryPhysicalCapacityBytes,
			},
		},
		Stats: stats,
	}

	return &rootInfo
}
