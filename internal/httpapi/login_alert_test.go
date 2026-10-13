package httpapi

import (
	"testing"
	"time"
)

func TestLoginAlertFiresAtThresholdWithoutUsername(t *testing.T) {
	handler := NewAuthHandler(nil)
	now := time.Now()
	handler.now = func() time.Time { return now }
	var addresses []string
	handler.SetLoginAlert(func(address string) { addresses = append(addresses, address) })
	for i := 0; i < 7; i++ {
		handler.recordFailureLimit("192.0.2.1\x00private-username", 5)
	}
	for i := 0; i < 5; i++ {
		handler.recordFailureLimit("ip\x00192.0.2.1", 5)
	}
	if len(addresses) != 1 || addresses[0] != "192.0.2.1" {
		t.Fatal("duplicate or sensitive login alert", addresses)
	}
	now = now.Add(16 * time.Minute)
	for i := 0; i < 5; i++ {
		handler.recordFailureLimit("192.0.2.1\x00private-username", 5)
	}
	if len(addresses) != 2 {
		t.Fatal("new lockout window did not alert", addresses)
	}
}
