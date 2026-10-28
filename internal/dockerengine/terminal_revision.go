package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// TerminalRevision 固定运行时及所属主机配置身份，不向调用方暴露认证内容。
func (s *Service) TerminalRevision(ctx context.Context, id string) (string, error) {
	endpoint, err := s.Find(ctx, id)
	if err != nil {
		return "", err
	}
	if !endpoint.IsActive {
		return "", ErrEndpointNotFound
	}
	revision, err := s.monitorRevision(ctx, endpoint)
	if err != nil {
		return "", err
	}
	value, err := json.Marshal([]any{endpoint.ID, endpoint.HostID, endpoint.Host, endpoint.TLSCiphertext,
		endpoint.SSHCredentialCiphertext, endpoint.SSHHostKeyFingerprint, endpoint.ProjectID,
		endpoint.DepartmentID, endpoint.UpdatedAt, revision.hostUpdatedAt})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}
