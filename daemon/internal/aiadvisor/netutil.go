package aiadvisor

import (
	"net"
	"strconv"
	"strings"
)

func splitHostPortCompat(endpoint string) (string, int, bool) {
	h, p, err := net.SplitHostPort(endpoint)
	if err != nil {
		idx := strings.LastIndex(endpoint, ":")
		if idx <= 0 || idx+1 >= len(endpoint) {
			return "", 0, false
		}
		h, p = endpoint[:idx], endpoint[idx+1:]
	}
	portNum, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil || portNum <= 0 || portNum > 65535 {
		return "", 0, false
	}
	return h, portNum, true
}

func joinHostPortCompat(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
