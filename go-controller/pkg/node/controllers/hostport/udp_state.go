// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/knftables"
)

const (
	udpStateV4Set = "hostports-udp4"
	udpStateV6Set = "hostports-udp6"
)

func udpStateSet(family string) *knftables.Set {
	if family == "ip" {
		return &knftables.Set{Name: udpStateV4Set, Type: "ipv4_addr . inet_service"}
	}
	return &knftables.Set{Name: udpStateV6Set, Type: "ipv6_addr . inet_service"}
}

// Keep active and pending tuples in the same transaction as the DNAT rules.
// This preserves cleanup work if the process exits after removing a rule but
// before deleting its conntrack entries. These sets are not used by packets.
func addUDPState(tx *knftables.Transaction, desired map[string]mapping, pending map[udpTuple]struct{}) {
	tuples := map[udpTuple]struct{}{}
	maps.Copy(tuples, pending)
	for _, m := range desired {
		if m.protocol == "udp" {
			tuples[udpTuple{m.family, m.hostIP, m.hostPort}] = struct{}{}
		}
	}
	for _, family := range []string{"ip", "ip6"} {
		set := udpStateSet(family)
		tx.Add(set)
		tx.Flush(set)
	}
	ordered := slices.Collect(maps.Keys(tuples))
	slices.SortFunc(ordered, func(a, b udpTuple) int {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	})
	for _, tuple := range ordered {
		ip := tuple.hostIP
		if ip == "" {
			ip = "0.0.0.0"
			if tuple.family == "ip6" {
				ip = "::"
			}
		}
		tx.Add(&knftables.Element{Set: udpStateSet(tuple.family).Name, Key: []string{ip, strconv.Itoa(int(tuple.port))}})
	}
}

func parseUDPTuple(family, address, port string) (udpTuple, error) {
	tuple := udpTuple{family: family}
	if family != "ip" && family != "ip6" {
		return tuple, fmt.Errorf("invalid UDP hostPort family %q", family)
	}
	if address != "" {
		ip := net.ParseIP(address)
		if ip == nil || (ip.To4() != nil) != (family == "ip") {
			return tuple, fmt.Errorf("invalid UDP hostPort address %q for %s", address, family)
		}
		if !ip.IsUnspecified() {
			tuple.hostIP = ip.String()
		}
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return tuple, fmt.Errorf("invalid UDP hostPort port %q", port)
	}
	tuple.port = int32(n)
	return tuple, nil
}

func (c *Controller) restoreUDPState(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	pending := map[udpTuple]struct{}{}
	missing := false
	for _, family := range []string{"ip", "ip6"} {
		elements, err := c.nft.ListElements(ctx, "set", udpStateSet(family).Name)
		if knftables.IsNotFound(err) {
			missing = true
			continue
		}
		if err != nil {
			return fmt.Errorf("read UDP hostPort state: %w", err)
		}
		for _, element := range elements {
			if len(element.Key) != 2 {
				return fmt.Errorf("invalid UDP hostPort state key %v", element.Key)
			}
			tuple, err := parseUDPTuple(family, element.Key[0], element.Key[1])
			if err != nil {
				return err
			}
			pending[tuple] = struct{}{}
		}
	}
	if missing {
		// The first implementation did not persist tuples. Recover its rules
		// before overwriting them, including mappings whose pods are gone.
		rules, err := c.nft.ListRules(ctx, mappingChain)
		if err != nil && !knftables.IsNotFound(err) {
			return fmt.Errorf("list existing hostPort rules: %w", err)
		}
		if len(rules) != 0 {
			tuples, err := c.readLegacyUDP(ctx)
			if err != nil {
				return err
			}
			for _, tuple := range tuples {
				pending[tuple] = struct{}{}
			}
		}
	}
	c.pendingUDP = pending
	c.udpStateLoaded = true
	return nil
}

func readLegacyUDPTuples(ctx context.Context, table string) ([]udpTuple, error) {
	// knftables.ListRules exposes handles/comments but not rule expressions.
	// Numeric JSON avoids nft's service-name resolution when recovering ports.
	output, err := exec.CommandContext(ctx, "nft", "--numeric", "--json", "list", "chain", "inet", table, mappingChain).Output()
	if err != nil {
		return nil, fmt.Errorf("read legacy UDP hostPort rules: %w", err)
	}
	return parseLegacyUDPTuples(output)
}

func parseLegacyUDPTuples(data []byte) ([]udpTuple, error) {
	var dump struct {
		NFTables []struct {
			Rule *struct {
				Chain string `json:"chain"`
				Expr  []struct {
					Match *struct {
						Op   string `json:"op"`
						Left struct {
							Payload *struct {
								Protocol string `json:"protocol"`
								Field    string `json:"field"`
							} `json:"payload"`
						} `json:"left"`
						Right json.RawMessage `json:"right"`
					} `json:"match"`
					DNAT *struct {
						Family string `json:"family"`
					} `json:"dnat"`
				} `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil, fmt.Errorf("decode legacy UDP hostPort rules: %w", err)
	}
	var tuples []udpTuple
	for _, object := range dump.NFTables {
		if object.Rule == nil || object.Rule.Chain != mappingChain {
			continue
		}
		var family, address, port string
		for _, expr := range object.Rule.Expr {
			if expr.DNAT != nil {
				family = expr.DNAT.Family
			}
			match := expr.Match
			if match == nil || match.Op != "==" || match.Left.Payload == nil {
				continue
			}
			payload := match.Left.Payload
			if payload.Protocol == "udp" && payload.Field == "dport" {
				port = string(match.Right)
			} else if (payload.Protocol == "ip" || payload.Protocol == "ip6") && payload.Field == "daddr" {
				if err := json.Unmarshal(match.Right, &address); err != nil {
					return nil, fmt.Errorf("decode legacy hostPort destination: %w", err)
				}
			}
		}
		if port != "" {
			tuple, err := parseUDPTuple(family, address, port)
			if err != nil {
				return nil, err
			}
			tuples = append(tuples, tuple)
		}
	}
	return tuples, nil
}
