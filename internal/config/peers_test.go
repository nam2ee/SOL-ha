package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPeers_Add(t *testing.T) {
	peers := &Peers{}

	peer1 := Peer{Name: "validator-1", IP: "192.168.1.10"}
	peer2 := Peer{Name: "validator-2", IP: "192.168.1.11"}

	peers.Add(peer1)
	peers.Add(peer2)

	assert.Equal(t, peer1, (*peers)["validator-1"])
	assert.Equal(t, peer2, (*peers)["validator-2"])
	assert.Len(t, *peers, 2)
}

func TestPeers_String(t *testing.T) {
	peers := &Peers{
		"validator-1": {Name: "validator-1", IP: "192.168.1.10"},
		"validator-2": {Name: "validator-2", IP: "192.168.1.11"},
	}

	result := peers.String()
	// Map iteration order is not guaranteed, so we need to check that both entries are present
	assert.Contains(t, result, "validator-1:192.168.1.10")
	assert.Contains(t, result, "validator-2:192.168.1.11")
	assert.True(t, strings.HasPrefix(result, "["))
	assert.True(t, strings.HasSuffix(result, "]"))

	// Test with empty peers
	emptyPeers := &Peers{}
	result = emptyPeers.String()
	assert.Equal(t, "[]", result)
}
