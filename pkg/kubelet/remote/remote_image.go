/*
Copyright 2016 The Kubernetes Authors.

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

package remote

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang/glog"
	"google.golang.org/grpc"

	internalapi "k8s.io/kubernetes/pkg/kubelet/apis/cri"
	runtimeapi "k8s.io/kubernetes/pkg/kubelet/apis/cri/v1alpha1/runtime"
	"k8s.io/kubernetes/pkg/kubelet/util"
)

// RemoteImageService is a gRPC implementation of internalapi.ImageManagerService.
type RemoteImageService struct {
	timeout     time.Duration
	imageClient runtimeapi.ImageServiceClient
}

// NewRemoteImageService creates a new internalapi.ImageManagerService.
func NewRemoteImageService(endpoint string, connectionTimeout time.Duration) (internalapi.ImageManagerService, error) {
	glog.V(3).Infof("Connecting to image service %s", endpoint)
	addr, dailer, err := util.GetAddressAndDialer(endpoint)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.Dial(addr, grpc.WithInsecure(), grpc.WithTimeout(connectionTimeout), grpc.WithDialer(dailer))
	if err != nil {
		glog.Errorf("Connect remote image service %s failed: %v", addr, err)
		return nil, err
	}

	return &RemoteImageService{
		timeout:     connectionTimeout,
		imageClient: runtimeapi.NewImageServiceClient(conn),
	}, nil
}

// ListImages lists available images.
func (r *RemoteImageService) ListImages(filter *runtimeapi.ImageFilter) ([]*runtimeapi.Image, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	resp, err := r.imageClient.ListImages(ctx, &runtimeapi.ListImagesRequest{
		Filter: filter,
	})
	if err != nil {
		glog.Errorf("ListImages with filter %q from image service failed: %v", filter, err)
		return nil, err
	}

	return resp.Images, nil
}

// ImageStatus returns the status of the image.
func (r *RemoteImageService) ImageStatus(image *runtimeapi.ImageSpec) (*runtimeapi.Image, error) {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	resp, err := r.imageClient.ImageStatus(ctx, &runtimeapi.ImageStatusRequest{
		Image: image,
	})
	if err != nil {
		glog.Errorf("ImageStatus %q from image service failed: %v", image.Image, err)
		return nil, err
	}

	if resp.Image != nil {
		if resp.Image.Id == "" || resp.Image.Size_ == 0 {
			errorMessage := fmt.Sprintf("Id or size of image %q is not set", image.Image)
			glog.Errorf("ImageStatus failed: %s", errorMessage)
			return nil, errors.New(errorMessage)
		}
	}

	return resp.Image, nil
}

// PullImage pulls an image with authentication config.
func (r *RemoteImageService) PullImage(image *runtimeapi.ImageSpec, auth *runtimeapi.AuthConfig) (string, error) {
	ctx, cancel := getContextWithCancel()
	defer cancel()

	resp, err := r.imageClient.PullImage(ctx, &runtimeapi.PullImageRequest{
		Image: image,
		Auth:  auth,
	})
	if err != nil {
		glog.Errorf("PullImage %q from image service failed: %v", image.Image, err)
		return "", err
	}

	if resp.ImageRef == "" {
		errorMessage := fmt.Sprintf("imageRef of image %q is not set", image.Image)
		glog.Errorf("PullImage failed: %s", errorMessage)
		return "", errors.New(errorMessage)
	}

	return resp.ImageRef, nil
}

// RemoveImage removes the image.
func (r *RemoteImageService) RemoveImage(image *runtimeapi.ImageSpec) error {
	ctx, cancel := getContextWithTimeout(r.timeout)
	defer cancel()

	_, err := r.imageClient.RemoveImage(ctx, &runtimeapi.RemoveImageRequest{
		Image: image,
	})
	if err != nil {
		glog.Errorf("RemoveImage %q from image service failed: %v", image.Image, err)
		return err
	}

	return nil
}

// ImageFsInfo returns information of the filesystem that is used to store images.
func (r *RemoteImageService) ImageFsInfo(req *runtimeapi.ImageFsInfoRequest) (*runtimeapi.ImageFsInfoResponse, error) {
	glog.Infof("DAPO REMOTEIMAGE IMAGEFSINFO")
	//return nil, fmt.Errorf("not implemented")

	var filesystems []*runtimeapi.FilesystemUsage

	//type FilesystemUsage struct {
	//// Timestamp in nanoseconds at which the information were collected. Must be > 0.
	//Timestamp int64 `protobuf:"varint,1,opt,name=timestamp,proto3" json:"timestamp,omitempty"`
	//// The underlying storage of the filesystem.
	//StorageId *StorageIdentifier `protobuf:"bytes,2,opt,name=storage_id,json=storageId" json:"storage_id,omitempty"`
	//// UsedBytes represents the bytes used for images on the filesystem.
	//// This may differ from the total bytes used on the filesystem and may not
	//// equal CapacityBytes - AvailableBytes.
	//UsedBytes *UInt64Value `protobuf:"bytes,3,opt,name=used_bytes,json=usedBytes" json:"used_bytes,omitempty"`
	//// InodesUsed represents the inodes used by the images.
	//// This may not equal InodesCapacity - InodesAvailable because the underlying
	//// filesystem may also be used for purposes other than storing images.
	//InodesUsed *UInt64Value `protobuf:"bytes,4,opt,name=inodes_used,json=inodesUsed" json:"inodes_used,omitempty"`
	//}
	filesystems = append(filesystems, &runtimeapi.FilesystemUsage{
		Timestamp:  time.Now().UnixNano(),
		UsedBytes:  &runtimeapi.UInt64Value{Value: 0},
		InodesUsed: &runtimeapi.UInt64Value{Value: 0},
	})

	return &runtimeapi.ImageFsInfoResponse{
		ImageFilesystems: filesystems,
	}, nil
}
