// Package mdns provides Multicast-DNS service publication and discovery
// for Hearth. The coordinator advertises itself as `_hearth._tcp.local`
// and workers find it on the same LAN without manual configuration.
//
// This package wraps github.com/grandcat/zeroconf with a minimal,
// Hearth-specific API: Publish (used by the coordinator) and Discover
// (used by workers when their bundle has no explicit address).
package mdns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the Hearth-reserved Bonjour service identifier.
const ServiceType = "_hearth._tcp"

// Domain is the mDNS local domain.
const Domain = "local."

// Service describes a discovered Hearth coordinator instance.
type Service struct {
	Instance string   // e.g. "imac.hearth"
	Host     string   // e.g. "imac.local."
	Port     int      // gRPC port
	Addrs    []string // dotted-quad / colonized IPv6
	Text     []string // raw TXT records
}

// Address returns the first address suitable for `host:port` dialling. It
// prefers IPv4 because home routers and Windows Firewall handle IPv4
// multicast more reliably than IPv6.
func (s Service) Address() string {
	for _, a := range s.Addrs {
		if !strings.Contains(a, ":") { // IPv4
			return fmt.Sprintf("%s:%d", a, s.Port)
		}
	}
	for _, a := range s.Addrs {
		return fmt.Sprintf("[%s]:%d", a, s.Port)
	}
	return ""
}

// Publish advertises a Hearth coordinator on the LAN. The returned shutdown
// function stops the announcement; it is safe to call multiple times.
//
// instance is the visible name (typically the hostname); port is the gRPC
// listening port; txt entries are advertised verbatim and may include
// human-readable hints like "version=0.0.0-dev" or "ca=hearth-home-ca".
func Publish(instance string, port int, txt []string) (shutdown func(), err error) {
	if instance == "" {
		return nil, errors.New("mdns: empty instance name")
	}
	server, err := zeroconf.Register(instance, ServiceType, Domain, port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("mdns publish: %w", err)
	}
	return server.Shutdown, nil
}

// Discover blocks until either timeout elapses or one matching service is
// observed, then continues collecting until timeout.
//
// Discover returns every observed service. Workers typically pick the
// first one (since a home LAN should have exactly one coordinator) but the
// list is exposed so callers can warn on multi-coordinator misconfigs.
func Discover(ctx context.Context, timeout time.Duration) ([]Service, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("mdns resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry, 8)
	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := resolver.Browse(browseCtx, ServiceType, Domain, entries); err != nil {
		return nil, fmt.Errorf("mdns browse: %w", err)
	}

	var out []Service
	for {
		select {
		case <-browseCtx.Done():
			return out, nil
		case e, ok := <-entries:
			if !ok {
				return out, nil
			}
			out = append(out, fromEntry(e))
		}
	}
}

func fromEntry(e *zeroconf.ServiceEntry) Service {
	addrs := make([]string, 0, len(e.AddrIPv4)+len(e.AddrIPv6))
	for _, a := range e.AddrIPv4 {
		addrs = append(addrs, a.String())
	}
	for _, a := range e.AddrIPv6 {
		addrs = append(addrs, a.String())
	}
	return Service{
		Instance: e.Instance,
		Host:     e.HostName,
		Port:     e.Port,
		Addrs:    addrs,
		Text:     append([]string(nil), e.Text...),
	}
}
