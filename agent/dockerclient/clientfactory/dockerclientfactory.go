// Copyright 2014-2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package clientfactory

import (
	"context"

	"github.com/aws/amazon-ecs-agent/agent/dockerclient"
	"github.com/aws/amazon-ecs-agent/agent/dockerclient/dockeriface"
	log "github.com/cihub/seelog"
	docker "github.com/fsouza/go-dockerclient"
	"github.com/pkg/errors"
)

const (
	// minAPIVersionKey is the docker.Env key for min API version
	// This is supported in Docker API versions >=1.25
	// https://docs.docker.com/engine/api/version-history/#v125-api-changes
	minAPIVersionKey = "MinAPIVersion"
	// apiVersionKey is the docker.Env key for API version
	apiVersionKey = "ApiVersion"
)

// Factory provides a collection of docker remote clients that include a
// recommended client version as well as a set of alternative supported
// docker clients.
type Factory interface {
	// GetDefaultClient returns a versioned client for the default version
	GetDefaultClient() (dockeriface.Client, error)

	// GetClient returns a client with the specified version or an error
	// if the client doesn't exist.
	GetClient(version dockerclient.DockerVersion) (dockeriface.Client, error)

	// FindSupportedAPIVersions returns a slice of agent-supported Docker API
	// versions. Versions are tested by making calls against the Docker daemon
	// and may occur either at Factory creation time or lazily upon invocation
	// of this function. The slice represents the intersection of
	// agent-supported versions and daemon-supported versions.
	FindSupportedAPIVersions() []dockerclient.DockerVersion

	// FindKnownAPIVersions returns a slice of Docker API versions that are
	// known to the Docker daemon. Versions are tested by making calls against
	// the Docker daemon and may occur either at Factory creation time or
	// lazily upon invocation of this function. The slice represents the
	// intersection of the API versions that the agent knows exist (but does
	// not necessarily fully support) and the versions that result in
	// successful responses by the Docker daemon.
	FindKnownAPIVersions() []dockerclient.DockerVersion

	// FindClientAPIVersion returns the client api version
	FindClientAPIVersion(dockeriface.Client) dockerclient.DockerVersion
}

type factory struct {
	endpoint string
	clients  map[dockerclient.DockerVersion]dockeriface.Client
}

// newVersionedClient is a variable such that the implementation can be
// swapped out for unit tests
var newVersionedClient = func(endpoint, version string) (dockeriface.Client, error) {
	return docker.NewVersionedClient(endpoint, version)
}

// NewFactory initializes a client factory using a specified endpoint.
func NewFactory(ctx context.Context, endpoint string) Factory {
	return &factory{
		endpoint: endpoint,
		clients:  findDockerVersions(ctx, endpoint),
	}
}

func (f *factory) GetDefaultClient() (dockeriface.Client, error) {
	return f.GetClient(getDefaultVersion())
}

func (f *factory) FindSupportedAPIVersions() []dockerclient.DockerVersion {
	var supportedVersions []dockerclient.DockerVersion
	for _, testVersion := range getAgentVersions() {
		_, err := f.GetClient(testVersion)
		if err != nil {
			continue
		}
		supportedVersions = append(supportedVersions, testVersion)
	}
	return supportedVersions
}

func (f *factory) FindKnownAPIVersions() []dockerclient.DockerVersion {
	var knownVersions []dockerclient.DockerVersion
	for _, testVersion := range dockerclient.GetKnownAPIVersions() {
		_, err := f.GetClient(testVersion)
		if err != nil {
			continue
		}
		knownVersions = append(knownVersions, testVersion)
	}
	return knownVersions
}

// FindClientAPIVersion returns the version of the client from the map
// TODO we should let go docker client return this version information
func (f *factory) FindClientAPIVersion(client dockeriface.Client) dockerclient.DockerVersion {
	for k, v := range f.clients {
		if v == client {
			return k
		}
	}

	return getDefaultVersion()
}

// getClient returns a client specified by the docker version. Its wrapped
// by GetClient so that it can do platform-specific magic
func (f *factory) getClient(version dockerclient.DockerVersion) (dockeriface.Client, error) {
	client, ok := f.clients[version]
	if ok {
		return client, nil
	} else {
		return nil, errors.New("docker client factory: client not found for docker version: " + string(version))
	}
}

// findDockerVersions loops over all known API versions and finds which ones
// are supported by the docker daemon on the host
func findDockerVersions(ctx context.Context, endpoint string) map[dockerclient.DockerVersion]dockeriface.Client {
	// if the client version returns a MinAPIVersion and APIVersion, then use it to return
	// all the Docker clients between MinAPIVersion and APIVersion, else try pinging
	// the clients in getKnownAPIVersions
	var minAPIVersion, apiVersion string
	// get a Docker client with the default supported version
	client, err := newVersionedClient(endpoint, string(minDockerAPIVersion))
	if err == nil {
		derivedCtx, cancel := context.WithTimeout(ctx, dockerclient.VersionTimeout)
		defer cancel()

		clientVersion, err := client.VersionWithContext(derivedCtx)
		if err == nil {
			// check if the docker.Env obj has MinAPIVersion key
			if clientVersion.Exists(minAPIVersionKey) {
				minAPIVersion = clientVersion.Get(minAPIVersionKey)
			}
			// check if the docker.Env obj has APIVersion key
			if clientVersion.Exists(apiVersionKey) {
				apiVersion = clientVersion.Get(apiVersionKey)
			}
		}
	}

	clients := make(map[dockerclient.DockerVersion]dockeriface.Client)
	for _, version := range dockerclient.GetKnownAPIVersions() {
		dockerClient, err := getDockerClientForVersion(endpoint, string(version), minAPIVersion, apiVersion)
		if err != nil {
			log.Infof("Unable to get Docker client for version %s: %v", version, err)
			continue
		}
		clients[version] = dockerClient
	}
	return clients
}

func getDockerClientForVersion(
	endpoint string,
	version string,
	minAPIVersion string,
	apiVersion string) (dockeriface.Client, error) {
	if minAPIVersion != "" && apiVersion != "" {
		lessThanMinCheck := "<" + minAPIVersion
		moreThanMaxCheck := ">" + apiVersion
		minVersionCheck, err := dockerclient.DockerAPIVersion(version).Matches(lessThanMinCheck)
		if err != nil {
			return nil, errors.Wrapf(err, "version detection using MinAPIVersion: unable to get min version: %s", minAPIVersion)
		}
		maxVersionCheck, err := dockerclient.DockerAPIVersion(version).Matches(moreThanMaxCheck)
		if err != nil {
			return nil, errors.Wrapf(err, "version detection using MinAPIVersion: unable to get max version: %s", apiVersion)
		}
		// do not add the version when it is less than min api version or greater
		// than api version
		if minVersionCheck || maxVersionCheck {
			return nil, errors.Errorf("version detection using MinAPIVersion: unsupported version: %s", version)
		}
	}
	client, err := newVersionedClient(endpoint, string(version))
	if err != nil {
		return nil, errors.Wrapf(err, "version detection check: unable to create Docker client for version: %s", version)
	}
	err = client.Ping()
	if err != nil {
		return nil, errors.Wrapf(err, "version detection check: failed to ping with Docker version: %s", string(version))
	}
	return client, nil
}
