//go:build windows

package commands

import "github.com/inhere/gofer/internal/servicemgr"

func testManagedBackend() servicemgr.Backend { return servicemgr.BackendWindowsTask }
