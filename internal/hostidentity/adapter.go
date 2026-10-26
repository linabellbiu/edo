package hostidentity

// ProbeScript exposes the same read-only identity probe for callers that already
// own a pinned SSH transport. It does not create another identity implementation.
func ProbeScript() string { return probeCommand }

// ParseProbe validates output from ProbeScript using the normal identity parser.
func ParseProbe(output string) (Identity, error) {
	if len(output) > maxOutputBytes {
		return Identity{}, ErrUnavailable
	}
	return parseIdentity(output)
}
