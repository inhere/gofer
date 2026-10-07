//go:build unix

package commands

import "github.com/gookit/gcli/v3"

func newServePlatformCommands() []*gcli.Command { return []*gcli.Command{newServeUpgradeHelperCmd()} }
