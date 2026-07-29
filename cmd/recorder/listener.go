package main

import (
	"fmt"
	"net"
	"strconv"
)

type listenFunc func(network, address string) (net.Listener, error)

func listenWithFallback(address string, listen listenFunc) (net.Listener, string, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || portText != "9000" {
		listener, listenErr := listen("tcp", address)
		return listener, address, listenErr
	}

	for port := 9000; port <= 65535; port++ {
		candidate := net.JoinHostPort(host, strconv.Itoa(port))
		listener, listenErr := listen("tcp", candidate)
		if listenErr == nil {
			return listener, candidate, nil
		}
		if !isAddressInUse(listenErr) {
			return nil, candidate, listenErr
		}
	}
	return nil, address, fmt.Errorf("no available TCP port found from 9000 through 65535")
}
