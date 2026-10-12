package config

import (
	"strings"
	"testing"
)

func TestDeliveryProxyValidation(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:10801", "https://proxy.example:443", "socks5://127.0.0.1:1080"} {
		if err := (&DeliveryConfig{Proxy: ok}).validate("p"); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"127.0.0.1:10801", "ftp://h:21", "http://"} {
		err := (&DeliveryConfig{Proxy: bad}).validate("p")
		if err == nil || !strings.Contains(err.Error(), "projects.p.delivery.proxy") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
