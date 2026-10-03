// Package portalorigin validates customer-facing origins without changing schemes.
package portalorigin

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func Normalize(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("portal origin must be a valid HTTP or HTTPS URL")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("portal origin must be without credentials, path, query or fragment")
	}
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("invalid portal port")
		}
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) {
			return "", fmt.Errorf("public origins must use HTTPS; HTTP is allowed for private or loopback IP addresses")
		}
	}
	return u.Scheme + "://" + u.Host, nil
}
