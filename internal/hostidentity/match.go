package hostidentity

import (
	"net/netip"
	"strings"
)

// Matches compares machine identities without relying on hostnames. The first
// comparable strong identity wins: hardware UUID, kernel boot ID, then machine
// ID. A conflicting strong identity cannot be overridden by a matching address.
// Addresses are only a fallback when neither side has a comparable strong ID.
func Matches(left, right Identity) bool {
	for _, pair := range [][2]string{
		{left.SystemUUID, right.SystemUUID},
		{left.BootID, right.BootID},
		{left.MachineID, right.MachineID},
	} {
		first, second := matchIdentityID(pair[0]), matchIdentityID(pair[1])
		if first != "" && second != "" {
			return first == second
		}
	}
	addresses := make(map[netip.Addr]struct{}, len(left.Addresses))
	for _, value := range left.Addresses {
		if address, ok := matchIdentityIP(value); ok {
			addresses[address] = struct{}{}
		}
	}
	for _, value := range right.Addresses {
		if address, ok := matchIdentityIP(value); ok {
			if _, exists := addresses[address]; exists {
				return true
			}
		}
	}
	return false
}

func matchIdentityID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "", "{", "", "}", "").Replace(value)
	if len(value) != 32 {
		return ""
	}
	valid, allF := false, true
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return ""
		}
		valid = valid || character != '0'
		allF = allF && character == 'f'
	}
	if !valid || allF {
		return ""
	}
	return value
}

func matchIdentityIP(value string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, false
	}
	address = address.Unmap()
	return address, !address.IsLoopback() && !address.IsUnspecified() && !address.IsLinkLocalUnicast() && !address.IsMulticast()
}
