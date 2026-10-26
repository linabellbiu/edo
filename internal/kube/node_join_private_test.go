package kube

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNodeJoinInternalTokenIsNotSerialized(t *testing.T) {
	data, err := json.Marshal(NodeJoin{ID: "join", Token: "K10private-bootstrap-token", Command: "reviewed command"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-bootstrap-token") || strings.Contains(string(data), `"Token"`) || strings.Contains(string(data), `"token"`) {
		t.Fatalf("internal join credential leaked through response: %s", data)
	}
}
