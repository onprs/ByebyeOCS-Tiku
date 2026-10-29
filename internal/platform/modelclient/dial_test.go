package modelclient

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
)

func TestGuardedDialTriesAllowedAddressesAndBlocksPrivateRanges(t *testing.T) {
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}, {IP: net.ParseIP("100.64.0.1")},
			{IP: net.ParseIP("169.254.169.254")}, {IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("8.8.8.8")}}, nil
	}
	var attempts []string
	peer := make(chan net.Conn, 1)
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		attempts = append(attempts, address)
		if len(attempts) == 1 {
			return nil, errors.New("连接失败")
		}
		client, server := net.Pipe()
		peer <- server
		return client, nil
	}
	conn, err := guardedDialWith(resolve, dial, false)(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	(<-peer).Close()
	if !reflect.DeepEqual(attempts, []string{"1.1.1.1:443", "8.8.8.8:443"}) {
		t.Fatalf("地址校验或回退异常：%v", attempts)
	}
}

func TestAllowedUpstreamIP(t *testing.T) {
	for _, value := range []string{"0.0.0.5", "10.1.2.3", "100.64.1.2", "127.0.0.1", "169.254.169.254", "192.168.1.1", "198.18.1.1", "240.0.0.1", "::1", "fc00::1", "fe80::1"} {
		if allowedUpstreamIP(net.ParseIP(value), false) {
			t.Fatalf("不应允许内部地址：%s", value)
		}
	}
	if !allowedUpstreamIP(net.ParseIP("127.0.0.1"), true) || !allowedUpstreamIP(net.ParseIP("1.1.1.1"), false) {
		t.Fatal("可用地址被错误拒绝")
	}
}
