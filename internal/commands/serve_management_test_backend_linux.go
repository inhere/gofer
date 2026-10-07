//go:build linux

package commands

import "github.com/inhere/gofer/internal/servicemgr"

func testManagedBackend() servicemgr.Backend { return servicemgr.BackendSystemdUser }
