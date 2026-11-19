// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package run

import (
	"testing"

	"github.com/kolapsis/palier/internal/docker"
)

func TestCompleted(t *testing.T) {
	job := func(status string, code int, policy string) docker.ContainerDetail {
		var d docker.ContainerDetail
		d.State.Status, d.State.ExitCode, d.HostConfig.RestartPolicy.Name = status, code, policy
		return d
	}
	for _, c := range []struct {
		name string
		d    docker.ContainerDetail
		want bool
	}{
		{"a migration that ended well", job("exited", 0, "no"), true},
		{"no restart policy at all", job("exited", 0, ""), true},
		{"a job that failed", job("exited", 1, "no"), false},
		{"a service that should be up", job("exited", 0, "unless-stopped"), false},
		{"running", job("running", 0, "no"), false},
	} {
		if got := completed(c.d); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
