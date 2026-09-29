package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

func tryAutoSync(c *gcli.Command, s *tracker.Store) {
	_ = c
	cfg, err := s.ReadConfig()
	if err != nil || !cfg.AutoSync {
		return
	}
	cli, err := trackerClient()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := tracker.SyncHTTPWithToken(ctx, s, strings.TrimRight(cli.BaseURL(), "/"), cli.Token()); err != nil {
		fmt.Fprintln(os.Stderr, "sync warning:", err)
	}
}

func trackerClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, os.Getenv("GOFER_SERVER_ADDR"), os.Getenv("GOFER_SERVER_TOKEN"))
}
