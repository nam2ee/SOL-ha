package gossip

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	solanagorpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/sol-strategies/solana-validator-ha/internal/config"
	"github.com/sol-strategies/solana-validator-ha/internal/rpc"
)

// State represents the state of the peers as seen by the solana network
type State struct {
	// PeerStatesRefreshedAt is the last time the peer states were refreshed
	PeerStatesRefreshedAt time.Time
	// peerStatesByName are the peers that are currently in the solana network, keyed by their name
	peerStatesByName               map[string]PeerState // these are the peers that are currently in the solana network, keyed by their name
	configPeers                    config.Peers
	activePubkey                   string
	selfIP                         string
	clusterRPC                     *rpc.Client
	logger                         *log.Logger
	missingGossipIPs               []string
	lastActivePeer                 PeerState
	activePeerLastSeenAt           time.Time
	LeaderlessSamplesCount         int
	delinquentSlotDistanceOverride config.DelinquentSlotDistanceOverride
	lastRefreshHadRPCError         bool
	// peerLastSeenAtByName tracks the last time each peer was seen in gossip, persists even when peer goes missing
	peerLastSeenAtByName map[string]time.Time
}

// PeerState represents the state of a peer as seen by the solana network
type PeerState struct {
	// Name is the vanity name of the peer
	Name string
	// IP is the IP address of the peer
	IP string
	// Pubkey is the public key of the peer
	Pubkey string
	// LastSeenAt is the last time the peer was seen by the solana network
	LastSeenAtUTC time.Time
	// LastSeenActive is true if the peer was the active validator when it was last seen
	LastSeenActive bool
	// IsRecentlyInGossip is true if the peer was recently in gossip
	IsRecentlyInGossip bool
}

// Options are the options for peers state
type Options struct {
	ClusterRPC                     *rpc.Client
	DelinquentSlotDistanceOverride config.DelinquentSlotDistanceOverride
	ActivePubkey                   string
	SelfIP                         string
	ConfigPeers                    config.Peers
	LogPrefix                      string
}

// NewState creates a new gossip state
func NewState(opts Options) *State {
	return &State{
		logger:                         log.WithPrefix(fmt.Sprintf("[%s gossip_state]", opts.LogPrefix)),
		clusterRPC:                     opts.ClusterRPC,
		activePubkey:                   opts.ActivePubkey,
		selfIP:                         opts.SelfIP,
		configPeers:                    opts.ConfigPeers,
		peerStatesByName:               make(map[string]PeerState),
		peerLastSeenAtByName:           make(map[string]time.Time),
		delinquentSlotDistanceOverride: opts.DelinquentSlotDistanceOverride,
	}
}

// Refresh the state of peers as seen by the solana network
func (p *State) Refresh() {
	p.logger.Debug("refreshing peers state")
	latestPeerStatesByName := make(map[string]PeerState)

	// get cluster nodes - if this fails we return an empty state, which should cause its consumer
	// to check for failovers
	clusterNodes, err := p.clusterRPC.GetClusterNodes(context.Background())
	if err != nil {
		p.lastRefreshHadRPCError = true
		p.peerStatesByName = latestPeerStatesByName
		p.PeerStatesRefreshedAt = time.Now().UTC()
		p.logger.Error("failed to get cluster nodes", "error", err)
		return
	}
	p.lastRefreshHadRPCError = false

	p.logger.Debug("looking for peers in gossip",
		"cluster_nodes_count", len(clusterNodes),
		"peers_count", len(p.configPeers),
		"peers", p.configPeers.String(),
		"active_pubkey", p.activePubkey,
	)

	// look through all the returned gossip nodes, looking for the ones that are in the config
	isLeaderlessSample := true
	for _, node := range clusterNodes {
		nodeIP := strings.Split(*node.Gossip, ":")[0]

		// if the peer is not the config, keep looking
		if !p.hasConfigPeerWithIP(nodeIP) {
			continue
		}

		// get the peer name from configPeers
		peerName, ok := p.peerNameFromIP(nodeIP)
		if !ok {
			p.logger.Warn("peer not found in config", "ip", nodeIP)
			continue
		}

		// Note: We trust gossip presence as a liveness indicator. The gossip protocol
		// uses UDP and has built-in expiration for stale entries (GOSSIP_PULL_CRDS_TIMEOUT_MS).
		// TCP probing the gossip port is unreliable since providers may block TCP while
		// allowing UDP, causing false negatives for healthy nodes.

		// lastSeenActive
		isActivePeer := node.Pubkey.String() == p.activePubkey

		// a borked active peer might appear in gossip but not actually be voting
		// so we need to check for that and only proceed to add it to the state if it is not voting still
		if isActivePeer && !p.isNodeActiveAndVoting(*node) {
			p.logger.Warn("active peer appears in gossip but is not voting - excluding from state", "ip", nodeIP, "pubkey", node.Pubkey.String())
			continue
		}

		// now we know the peer is in gossip and voting (if it is an active node) - so we can add it to the state

		// add the peer to the peerEntries
		peerState := PeerState{
			Name:               peerName,
			IP:                 nodeIP,
			LastSeenAtUTC:      time.Now().UTC(),
			Pubkey:             node.Pubkey.String(),
			LastSeenActive:     isActivePeer,
			IsRecentlyInGossip: slices.Contains(p.missingGossipIPs, nodeIP),
		}

		// register the peer state
		latestPeerStatesByName[peerName] = peerState

		// track last seen time for this peer (persists even when peer goes missing)
		p.peerLastSeenAtByName[peerName] = peerState.LastSeenAtUTC

		// update state's activePeerLastSeenAt
		if peerState.LastSeenActive {
			p.activePeerLastSeenAt = peerState.LastSeenAtUTC
			isLeaderlessSample = false
		}

		// log if is change of active peer
		if peerState.LastSeenActive && p.lastActivePeer.IP != "" && p.lastActivePeer.IP != peerState.IP {
			p.logger.Warn(fmt.Sprintf("active peer changed: %s (%s) -> %s (%s)",
				p.lastActivePeer.IP,
				p.lastActivePeer.Name,
				peerState.IP,
				peerState.Name,
			))
		}

		// register the peer if active
		if peerState.LastSeenActive {
			p.lastActivePeer = peerState
			p.logger.Debug("active peer found",
				"name", peerState.Name,
				"ip", peerState.IP,
				"pubkey", peerState.Pubkey,
				"is_active", peerState.LastSeenActive,
				"last_seen_at", peerState.LastSeenAtString(),
			)
		}

		// tell us what we found
		// state didn't have this peer last time but now it does - so we need to log that
		if !p.HasIP(peerState.IP) {
			p.logger.Info("peer found",
				"name", peerState.Name,
				"ip", peerState.IP,
				"pubkey", peerState.Pubkey,
				"is_active", peerState.LastSeenActive,
				"last_seen_at", peerState.LastSeenAtString(),
			)
		}

		// if all peers from configPeers are in the peerEntries, we can stop looking
		if len(p.configPeers) == len(latestPeerStatesByName) {
			break
		}
	}

	// warn if any of the config peers are not in the peerEntries
	latestMissingGossipIPs := []string{}
	for name, peer := range p.configPeers {
		if _, ok := latestPeerStatesByName[name]; !ok {
			latestMissingGossipIPs = append(latestMissingGossipIPs, peer.IP)
		}
	}

	// warn when peer transitions from present to missing (was in old state, now missing)
	for _, ip := range latestMissingGossipIPs {
		name, ok := p.peerNameFromIP(ip)
		if !ok {
			continue
		}

		// warn if peer was in the old state but is now missing
		if p.HasIP(ip) {
			lastSeenAt := ""
			if lastSeen, ok := p.peerLastSeenAtByName[name]; ok {
				lastSeenAt = lastSeen.Format(time.RFC3339Nano)
			}
			p.logger.Warn("peer lost", "name", name, "ip", ip, "last_seen_at", lastSeenAt)
			continue
		}

		// warn if it is the first time we've seen this peer missing from gossip
		if !slices.Contains(p.missingGossipIPs, ip) {
			p.logger.Warn("peer not found", "name", name, "ip", ip)
			continue
		}

		// peer _still_ missing from gossip - debug
		p.logger.Debug("peer still missing", "name", name, "ip", ip)
	}

	// update state
	if isLeaderlessSample {
		p.LeaderlessSamplesCount++
		p.logger.Warn("no active peer found",
			"leaderless_samples_count", p.LeaderlessSamplesCount)
	} else {
		p.LeaderlessSamplesCount = 0
	}
	p.missingGossipIPs = latestMissingGossipIPs
	p.peerStatesByName = latestPeerStatesByName
	p.PeerStatesRefreshedAt = time.Now().UTC()
	p.logger.Info(p.peersStateString())
}

// peersStateString returns a string representation of all configured peers for logging
// format: discovered N/<total_configured_peers> configured peers: [<emoji> <active|passive|missing> <peer_name> <peer_ip> rank=<peer_ip_rank>/<total_ip_ranks> last_seen_at=<peer_last_seen_at_utc_with_nanoseconds>] ...
// where emoji is 🟢 for active, 🟡 for passive, and 🔴 for missing
// displayed in ascending order of ip rank
func (p *State) peersStateString() string {
	if len(p.configPeers) == 0 {
		return ""
	}

	// Collect configured peers and sort by IP
	type peerInfo struct {
		name       string
		ip         string
		discovered bool
		active     bool
		lastSeenAt string
	}

	peers := make([]peerInfo, 0, len(p.configPeers))
	discoveredCount := 0
	for name, configPeer := range p.configPeers {
		info := peerInfo{
			name: name,
			ip:   configPeer.IP,
		}
		if state, ok := p.peerStatesByName[name]; ok {
			info.discovered = true
			info.active = state.LastSeenActive
			info.lastSeenAt = state.LastSeenAtUTC.Format(time.RFC3339Nano)
			discoveredCount++
		} else if lastSeen, ok := p.peerLastSeenAtByName[name]; ok {
			// peer is missing but we have a record of when it was last seen
			info.lastSeenAt = lastSeen.Format(time.RFC3339Nano)
		}
		peers = append(peers, info)
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].ip < peers[j].ip
	})

	// Build output
	var sb strings.Builder
	numPeers := len(peers)

	fmt.Fprintf(&sb, "discovered %d/%d configured peers", discoveredCount, numPeers)

	for rank, peer := range peers {
		// Determine status and emoji: active (🟢), passive (🟡), or missing (🔴)
		var emoji, status string
		if peer.discovered {
			if peer.active {
				emoji = "🟢"
				status = "active"
			} else {
				emoji = "🟡"
				status = "passive"
			}
		} else {
			emoji = "🔴"
			status = "missing"
		}

		sb.WriteByte(' ')
		fmt.Fprintf(&sb, "[%s %s %s %s rank=%d/%d last_seen_at=%s]",
			emoji, status, peer.name, peer.ip, rank, numPeers-1, peer.lastSeenAt)
	}

	return sb.String()
}

// PeerIPRankMap returns a map of IP addresses to their zero-indexed rank in the sorted list of IPs
func (p *State) PeerIPRankMap() map[string]int {
	ipRankMap := make(map[string]int)
	for ipIndex, ip := range p.getSortedIPs() {
		ipRankMap[ip] = ipIndex
	}
	return ipRankMap
}

// PeerCount returns the number of peers in the gossip state
func (p *State) PeerCount() int {
	return len(p.peerStatesByName)
}

// getSortedIPs returns a an ascendings ordered list of IP addresses from the peerStatesByName map
func (p *State) getSortedIPs() []string {
	ips := []string{}
	for _, peerState := range p.peerStatesByName {
		ips = append(ips, peerState.IP)
	}
	sort.Strings(ips)
	return ips
}

// isNodeActiveAndVoting returns true if the node is active and voting
func (p *State) isNodeActiveAndVoting(node solanagorpc.GetClusterNodesResult) bool {
	// get the current slot
	currentSlot, err := p.clusterRPC.GetSlot(context.Background())
	if err != nil {
		p.logger.Error("failed to get current slot", "error", err)
		return true // forgive rpc error and assume innocence lest we trigger a false-positive failover
	}

	// configure get vote accounts options
	getVoteAccountsOpts := solanagorpc.GetVoteAccountsOpts{
		Commitment: solanagorpc.CommitmentProcessed,
	}

	// if configured, override the sdk delinquent slot distance value with a config-supplied value
	if p.delinquentSlotDistanceOverride.Enabled {
		getVoteAccountsOpts.DelinquentSlotDistance = &p.delinquentSlotDistanceOverride.Value
	}

	// get vote accounts to look for our node within
	voteAccounts, err := p.clusterRPC.GetVoteAccounts(context.Background(), &getVoteAccountsOpts)
	if err != nil {
		p.logger.Error("failed to get vote accounts", "error", err)
		return true // forgive rpc error and assume innocence lest we trigger a false-positive failover
	}

	// if the node is in the delinquent list - it is not voting, but forgive delinquency due to low balance
	// because failing over in this case definitely won't fix things anyway
	for _, delinquentVoteAccount := range voteAccounts.Delinquent {
		// not us - keep looking
		if !delinquentVoteAccount.NodePubkey.Equals(node.Pubkey) {
			continue
		}

		// ok we might be legit delinquent but let's check if the node's identity balance is below the rent-exempt balance
		balance, err := p.clusterRPC.GetBalance(context.Background(), delinquentVoteAccount.NodePubkey)
		if err != nil {
			p.logger.Error("failed to get balance", "error", err)
			return true // forgive rpc error and assume innocence lest we trigger a false-positive failover
		}
		// rent exempt min is 890880 lamports
		if balance.Value <= 890880 {
			p.logger.Error("‼️ node is delinquent from balance being below rent-exempt minimum - assuming still active to not trigger a false-positive failover - FIX balance pronto!",
				"gossip_address", *node.Gossip,
				"pubkey", node.Pubkey.String(),
				"current_slot", currentSlot,
				"balance", balance.Value,
			)
			return true
		}

		// ohhh shit! we're delinquent - snitch on this guy!
		p.logger.Error(fmt.Sprintf("‼️ node is delinquent - not voting (%d slots behind)", currentSlot-delinquentVoteAccount.LastVote),
			"gossip_address", *node.Gossip,
			"pubkey", node.Pubkey.String(),
			"current_slot", currentSlot,
			"last_voted_at_slot", delinquentVoteAccount.LastVote,
		)
		return false
	}

	// good good, node is not delinquent, let's see if it is voting
	var nodeVoteAccount *solanagorpc.VoteAccountsResult
	found := false

	// try to find our node in the retrieved current vote accounts
	for _, voteAccount := range voteAccounts.Current {
		// not us - keep looking
		if !voteAccount.NodePubkey.Equals(node.Pubkey) {
			continue
		}

		// it is us - let's see wtf is gong on
		found = true
		nodeVoteAccount = &voteAccount
		break
	}

	// if we didn't find our node - we're definitely inactive and not voting
	if !found {
		p.logger.Warn("no current or delinquent vote account found for node",
			"gossip_address", *node.Gossip,
			"pubkey", node.Pubkey.String(),
			"current_slot", currentSlot,
		)
		return false
	}

	// found us
	p.logger.Debug("node found in current vote accounts",
		"gossip_address", *node.Gossip,
		"pubkey", node.Pubkey.String(),
		"vote_account_pubkey", nodeVoteAccount.VotePubkey.String(),
		"last_voted_at_slot", nodeVoteAccount.LastVote,
		"current_slot", currentSlot,
	)

	return true
}

// HasActivePeer returns true if any of the peers are the active validator
func (p *State) HasActivePeer() bool {
	for name, peer := range p.peerStatesByName {
		if peer.LastSeenActive {
			p.logger.Debug(fmt.Sprintf("active peer found - last seen at %s", peer.LastSeenAtString()), "name", name, "ip", peer.IP, "pubkey", peer.Pubkey)
			return true
		}
	}
	return false
}

// LeaderlessSamplesExceedsThreshold allows for up to n samples without an active peer before declaring leaderless
func (p *State) LeaderlessSamplesExceedsThreshold(n int) bool {
	return p.LeaderlessSamplesCount >= n
}

// LeaderlessSamplesBelowThreshold allows for up to n samples without an active peer before declaring leaderless
func (p *State) LeaderlessSamplesBelowThreshold(n int) bool {
	return p.LeaderlessSamplesCount < n
}

// HasIP returns true if the IP is in the peers gossip state
func (p *State) HasIP(ip string) bool {
	for _, peer := range p.peerStatesByName {
		if peer.IP == ip {
			return true
		}
	}
	return false
}

// GetActivePeer returns the active peer state
func (p *State) GetActivePeer() (state PeerState, err error) {
	for _, state := range p.peerStatesByName {
		if state.LastSeenActive {
			return state, nil
		}
	}
	return PeerState{}, fmt.Errorf("no active peer found")
}

// HasPeers returns true if the IP has any peers in the gossip state
// that is, any peers in that state that are not the passed IP address
func (p *State) HasPeers(ip string) bool {
	// if the self IP is in the gossip state, we have peers
	for _, peer := range p.peerStatesByName {
		if peer.IP != ip {
			return true
		}
	}
	return false
}

// LastRefreshHadRPCError returns true if the last Refresh() call failed due to RPC error
func (p *State) LastRefreshHadRPCError() bool {
	return p.lastRefreshHadRPCError
}

// GetPeerStates returns the current peer states
func (p *State) GetPeerStates() map[string]PeerState {
	return p.peerStatesByName
}

// LastSeenAtString returns the last seen at time as a string
func (p *PeerState) LastSeenAtString() string {
	return p.LastSeenAtUTC.Format(time.RFC3339)
}

func (p *State) peerNameFromIP(ip string) (string, bool) {
	for name, peer := range p.configPeers {
		if peer.IP == ip {
			return name, true
		}
	}
	return "", false
}

func (p *State) hasConfigPeerWithIP(ip string) bool {
	for _, peer := range p.configPeers {
		if peer.IP == ip {
			return true
		}
	}
	return false
}

// IPEquals returns true if the IP is equal to the peer's IP
func (p *PeerState) IPEquals(ip string) bool {
	return p.IP == ip
}
