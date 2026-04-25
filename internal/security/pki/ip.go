package pki

import "net"

// parseIP returns nil for the empty string and for syntactically invalid
// addresses. Both cases are skipped silently when building SANs.
func parseIP(s string) net.IP {
	if s == "" {
		return nil
	}
	return net.ParseIP(s)
}
