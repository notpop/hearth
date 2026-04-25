package mdns_test

import (
	"testing"

	"github.com/notpop/hearth/internal/adapter/discovery/mdns"
)

func TestServiceAddressPrefersIPv4(t *testing.T) {
	s := mdns.Service{
		Port:  7843,
		Addrs: []string{"fe80::1", "192.168.1.10"},
	}
	if got := s.Address(); got != "192.168.1.10:7843" {
		t.Errorf("got %q", got)
	}
}

func TestServiceAddressFallsBackToIPv6(t *testing.T) {
	s := mdns.Service{Port: 7843, Addrs: []string{"fe80::1"}}
	if got := s.Address(); got != "[fe80::1]:7843" {
		t.Errorf("got %q", got)
	}
}

func TestServiceAddressEmpty(t *testing.T) {
	s := mdns.Service{Port: 7843}
	if got := s.Address(); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestPublishRejectsEmptyInstance(t *testing.T) {
	if _, err := mdns.Publish("", 1234, nil); err == nil {
		t.Errorf("expected error")
	}
}
