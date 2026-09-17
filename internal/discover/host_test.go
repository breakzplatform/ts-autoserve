package discover

import "testing"

func TestLocalPortAcceptsLocalBinds(t *testing.T) {
	for _, addr := range []string{
		"*:3000",
		"0.0.0.0:3000",
		"[::]:3000",
		"127.0.0.1:5173",
		"127.0.1.1:5173",
		"[::1]:5173",
		"[::1%lo0]:5173", // macOS lsof appends the interface zone
		"[::ffff:127.0.0.1]:8080",
		"localhost:8080",
	} {
		if _, ok := localPort(addr); !ok {
			t.Errorf("localPort(%q) rejected a local bind", addr)
		}
	}
}

func TestLocalPortRejectsOtherBinds(t *testing.T) {
	for _, addr := range []string{
		"192.168.1.20:3000", // one LAN address: not ours to publish
		"[fe80::1%en0]:3000",
		"127.0.0.1:0",
		"127.0.0.1:70000",
		"127.0.0.1:*",
		"3000",
		"",
	} {
		if port, ok := localPort(addr); ok {
			t.Errorf("localPort(%q) = %d, want rejected", addr, port)
		}
	}
}
