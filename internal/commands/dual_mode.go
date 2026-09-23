package commands

import (
	"fmt"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// dual_mode.go holds the ONE decision behind every "the local copy or the server's?"
// command (`agent list`, `agent skill …`, `project show/validate`): a box that has no
// local server config has no local library or registry to work on either, so the
// command must answer from the server rather than fail with a missing-config error
// the operator never asked for (bd h-aii-uzvc: `agent skill ls` on a
// GOFER_RUN_MODE=worker node refused with "no local gofer config found … drop
// --local" although --local had not been passed).

// serverAPIChoice is the outcome of that decision. remote=true means the command
// talks to the server; fallback=true means it does so NOT because the node's role is
// client but because this box has no local server config — the case where a failed
// call has to explain the detour.
type serverAPIChoice struct {
	remote   bool
	fallback bool
}

// useServerAPI decides whether a dual-mode command reads the SERVER's API:
//
//  1. --local always wins (the escape hatch for a client node inspecting the copy on
//     that box);
//  2. a client node (GOFER_RUN_MODE=client) has no local config by design → server;
//  3. any other node WITHOUT a local server config (a worker/container that only runs
//     jobs for a hub) → server, as a fallback;
//  4. otherwise the local config.
func useServerAPI(localFlag bool) serverAPIChoice {
	if localFlag {
		return serverAPIChoice{}
	}
	if config.IsClientRunMode() {
		return serverAPIChoice{remote: true}
	}
	if hasLocalServerConfig() {
		return serverAPIChoice{}
	}
	return serverAPIChoice{remote: true, fallback: true}
}

// hasLocalServerConfig reports whether this box holds a local gofer server config —
// the config.yaml that owns <config-dir>/skills and the metadata db, i.e. what the
// local path of a dual-mode command reads.
//
// A RESOLVED config file is normally enough: a fresh server has no projects yet and
// still owns its (empty) library. On a non-server node a config without projects is
// only a connection stub (`gofer init` writes server.addr and little else), so it does
// not count there — on such a box the server's library is the one the operator means.
func hasLocalServerConfig() bool {
	cfg, path, err := config.Load(config.InputCfgFile)
	if err != nil {
		// A config file that exists but cannot be read/decoded is still a LOCAL
		// config: the local path must surface that error rather than quietly
		// answering from the server.
		return true
	}
	if path == "" {
		return false
	}
	return len(cfg.Projects) > 0 || config.RunMode() == config.RunModeServer
}

// client builds the HTTP client for a command that decided to go remote, wrapping a
// failure with the reason it went there in the first place.
func (ch serverAPIChoice) client() (*client.Client, error) {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return nil, ch.wrap(err)
	}
	return cli, nil
}

// wrap explains a failed server call when the command only went remote because this
// box has no local server config: without it the operator reads a bare connection
// error for a command they expected to work locally. Only TRANSPORT failures get the
// prefix — a server that answered (any HTTP status) tells its own story, and
// "connection failed" would be a lie over a 404.
func (ch serverAPIChoice) wrap(err error) error {
	if err == nil || !ch.fallback || client.StatusOf(err) != 0 {
		return err
	}
	return fmt.Errorf("本机没有 server 配置（运行模式=%s），且连接 server 失败：%w", config.RunMode(), err)
}
