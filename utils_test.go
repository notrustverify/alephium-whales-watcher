package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestTokenRefreshKeepsLastValidSnapshot(t *testing.T) {
	oldURL := parameters.TokenListUrl
	defer func() { parameters.TokenListUrl = oldURL }()
	var payloadMu sync.Mutex
	payload := `{"tokens":[{"id":"token","symbol":"TEST","decimals":18}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payloadMu.Lock()
		defer payloadMu.Unlock()
		w.Write([]byte(payload))
	}))
	defer server.Close()
	parameters.TokenListUrl = server.URL
	updateTokens()
	if token := searchTokenData("token"); token.Symbol != "TEST" || token.Decimals != 18 {
		t.Fatalf("unexpected metadata: %+v", token)
	}
	payloadMu.Lock()
	payload = `{"tokens":`
	payloadMu.Unlock()
	updateTokens()
	if searchTokenData("token").Symbol != "TEST" {
		t.Fatal("invalid refresh replaced valid metadata")
	}
	payloadMu.Lock()
	payload = `{"tokens":[{"id":"token","symbol":"UPDATED","decimals":6}]}`
	payloadMu.Unlock()
	var readers sync.WaitGroup
	for i := 0; i < 10; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 1000; j++ {
				token := searchTokenData("token")
				if token.Symbol != "TEST" && token.Symbol != "UPDATED" {
					t.Errorf("partial snapshot: %+v", token)
					return
				}
			}
		}()
	}
	updateTokens()
	readers.Wait()
	if searchTokenData("token").Symbol != "UPDATED" {
		t.Fatal("valid refresh was not published")
	}
}

func TestFullnodeBaseURL(t *testing.T) {
	for _, tc := range []struct {
		address   string
		websocket bool
		want      string
	}{
		{"127.0.0.1:12973", false, "http://127.0.0.1:12973"},
		{"localhost:12973", false, "http://localhost:12973"},
		{"[::1]:12973", false, "http://[::1]:12973"},
		{"node.mainnet.alephium.org", false, "https://node.mainnet.alephium.org"},
		{"http://192.168.1.2:12973/", false, "http://192.168.1.2:12973"},
		{"https://127.0.0.1:12973", false, "https://127.0.0.1:12973"},
		{"127.0.0.1:11973", true, "ws://127.0.0.1:11973"},
		{"node.mainnet.alephium.org", true, "wss://node.mainnet.alephium.org"},
		{"ws://192.168.1.2:11973/", true, "ws://192.168.1.2:11973"},
	} {
		if got := fullnodeBaseURL(tc.address, tc.websocket); got != tc.want {
			t.Errorf("fullnodeBaseURL(%q, %v) = %q; want %q", tc.address, tc.websocket, got, tc.want)
		}
	}
}
