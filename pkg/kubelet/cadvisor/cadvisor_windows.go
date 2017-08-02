// +build windows

/*
Copyright 2015 The Kubernetes Authors.

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

package cadvisor

import (
	"github.com/golang/glog"
	"github.com/google/cadvisor/events"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"k8s.io/kubernetes/pkg/kubelet/winstats"
)

type cadvisorClient struct {
	winStatsClient *winstats.Client
}

var _ Interface = new(cadvisorClient)

// New creates a cAdvisor and exports its API on the specified port if port > 0.
func New(address string, port uint, runtime string, rootPath string) (Interface, error) {
	glog.Infof("CREATING A NEW CADVISOR CLIENT")

	client, err := winstats.NewClient()

	return &cadvisorClient{winStatsClient: client}, err
}

func (cu *cadvisorClient) Start() error {
	glog.Infof("c_advisor start()")
	return nil
}

func (cu *cadvisorClient) DockerContainer(name string, req *cadvisorapi.ContainerInfoRequest) (cadvisorapi.ContainerInfo, error) {
	return cadvisorapi.ContainerInfo{}, nil
}

func (cu *cadvisorClient) ContainerInfo(name string, req *cadvisorapi.ContainerInfoRequest) (*cadvisorapi.ContainerInfo, error) {
	glog.Infof("c_advisor containerInfo")

	return &cadvisorapi.ContainerInfo{}, nil
}

func (cu *cadvisorClient) ContainerInfoV2(name string, options cadvisorapiv2.RequestOptions) (map[string]cadvisorapiv2.ContainerInfo, error) {

	//GetWinHelper().Start()
	//glog.Info("c_advisor going to call win helper")
	//glog.Infof("c_advisor containerInfo V2")

	//m := make(map[string]cadvisorapiv2.ContainerInfo)
	//stats := make([]*cadvisorapiv2.ContainerStats, 1)
	//// cpuStats := cadvisorapi.CpuStats{Usage: cadvisorapi.CpuUsage{Total: 50}}
	//stats[0] = &cadvisorapiv2.ContainerStats{Cpu: &cadvisorapi.CpuStats{Usage: cadvisorapi.CpuUsage{Total: GetWinHelper().GetUsageCoreNanoSeconds()}}}
	//m["/"] = cadvisorapiv2.ContainerInfo{Spec: cadvisorapiv2.ContainerSpec{Namespace: "testNameSpace", Image: "davidImage", HasCpu: true}, Stats: stats}
	//return m, nil
	//return make(map[string]cadvisorapiv2.ContainerInfo), nil
	return cu.winStatsClient.WinContainerInfos(), nil
}

func (cu *cadvisorClient) SubcontainerInfo(name string, req *cadvisorapi.ContainerInfoRequest) (map[string]*cadvisorapi.ContainerInfo, error) {
	glog.Infof("c_advisor subcontainerInfo")

	return nil, nil
}

func (cu *cadvisorClient) MachineInfo() (*cadvisorapi.MachineInfo, error) {
	return &cadvisorapi.MachineInfo{
		MemoryCapacity: 32000000000, // 32 GB
	}, nil
}

func (cu *cadvisorClient) VersionInfo() (*cadvisorapi.VersionInfo, error) {
	return &cadvisorapi.VersionInfo{}, nil
}

func (cu *cadvisorClient) ImagesFsInfo() (cadvisorapiv2.FsInfo, error) {
	glog.Infof("c_advisor imagesFSInfo")

	return cadvisorapiv2.FsInfo{}, nil
}

func (cu *cadvisorClient) RootFsInfo() (cadvisorapiv2.FsInfo, error) {
	glog.Infof("c_advisor rootFSInfo")

	return cadvisorapiv2.FsInfo{}, nil
}

func (cu *cadvisorClient) WatchEvents(request *events.Request) (*events.EventChannel, error) {
	return &events.EventChannel{}, nil
}

func (cu *cadvisorClient) HasDedicatedImageFs() (bool, error) {
	return false, nil
}
