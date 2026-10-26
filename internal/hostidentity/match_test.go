package hostidentity

import "testing"

func TestMatchesUsesStrongIdentityPriorityAndNormalizesInput(t *testing.T) {
	const first = "0123456789abcdef0123456789abcdef"
	const second = "abcdef0123456789abcdef0123456789"
	const third = "1234567890abcdef1234567890abcdef"
	for _, scenario := range []struct {
		name        string
		left, right Identity
		matches     bool
	}{
		{"hardware-UUID-normalization", Identity{SystemUUID: " {01234567-89AB-CDEF-0123-456789ABCDEF} "}, Identity{SystemUUID: first}, true},
		{"hardware-beats-conflicting-boot-and-machine", Identity{SystemUUID: first, BootID: second, MachineID: third}, Identity{SystemUUID: first, BootID: third, MachineID: second}, true},
		{"hardware-conflict-cannot-use-boot-or-IP", Identity{SystemUUID: first, BootID: third, Addresses: []string{"192.0.2.1"}}, Identity{SystemUUID: second, BootID: third, Addresses: []string{"192.0.2.1"}}, false},
		{"boot-normalization", Identity{BootID: "{01234567-89AB-CDEF-0123-456789ABCDEF}"}, Identity{BootID: first}, true},
		{"shared-kernel-before-machine", Identity{BootID: first, MachineID: second}, Identity{BootID: first, MachineID: third}, true},
		{"boot-conflict-cannot-use-machine-or-IP", Identity{BootID: first, MachineID: third, Addresses: []string{"192.0.2.1"}}, Identity{BootID: second, MachineID: third, Addresses: []string{"192.0.2.1"}}, false},
		{"machine-normalization", Identity{MachineID: "01234567-89AB-CDEF-0123-456789ABCDEF"}, Identity{MachineID: first}, true},
		{"machine-conflict-cannot-use-IP", Identity{MachineID: first, Addresses: []string{"192.0.2.1"}}, Identity{MachineID: second, Addresses: []string{"192.0.2.1"}}, false},
		{"missing-one-hardware-ID-uses-next-comparable-ID", Identity{SystemUUID: first, MachineID: second}, Identity{MachineID: second}, true},
		{"missing-one-strong-ID-uses-IP", Identity{SystemUUID: first, Addresses: []string{"192.0.2.1"}}, Identity{Addresses: []string{"192.0.2.1"}}, true},
		{"mapped-IPv4", Identity{Addresses: []string{" ::ffff:192.0.2.1 "}}, Identity{Addresses: []string{"192.0.2.1"}}, true},
		{"IPv6", Identity{Addresses: []string{"2001:0db8::1"}}, Identity{Addresses: []string{"2001:db8::1"}}, true},
		{"different-addresses", Identity{Addresses: []string{"192.0.2.1"}}, Identity{Addresses: []string{"192.0.2.2"}}, false},
		{"no-membership-evidence", Identity{}, Identity{}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := Matches(scenario.left, scenario.right); got != scenario.matches {
				t.Fatalf("match=%v, want %v", got, scenario.matches)
			}
			if got := Matches(scenario.right, scenario.left); got != scenario.matches {
				t.Fatalf("reverse match=%v, want %v", got, scenario.matches)
			}
		})
	}
}

func TestMatchesRejectsPlaceholderIDsAndNonMembershipAddresses(t *testing.T) {
	for _, value := range []string{"", "00000000-0000-0000-0000-000000000000", "FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF", "{invalid}", "0123456789abcdef0123456789abcdeg", "{ 0123456789abcdef0123456789abcdef }"} {
		t.Run("ID-"+value, func(t *testing.T) {
			identity := Identity{SystemUUID: value, BootID: value, MachineID: value}
			if Matches(identity, identity) {
				t.Fatal("invalid or placeholder IDs were accepted as identity evidence")
			}
			identity.Addresses = []string{"192.0.2.1"}
			if !Matches(identity, Identity{Addresses: []string{"192.0.2.1"}}) {
				t.Fatal("an invalid strong ID incorrectly prevented the address fallback")
			}
		})
	}
	for _, value := range []string{"", "invalid", "192.0.2.1/24", "127.0.0.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::", "169.254.1.2", "fe80::1", "224.0.0.1", "ff02::1"} {
		t.Run("IP-"+value, func(t *testing.T) {
			identity := Identity{Addresses: []string{value}}
			if Matches(identity, identity) {
				t.Fatal("non-membership address was accepted as identity evidence")
			}
		})
	}
}
