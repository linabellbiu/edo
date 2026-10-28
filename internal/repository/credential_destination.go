package repository

import (
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"edo/internal/model"
)

type credentialDestination struct {
	protocol string
	host     string
	port     int
	sshUser  string
}

func repositoryCredentialDestination(rawURL string) (credentialDestination, error) {
	endpoint, err := transport.NewEndpoint(rawURL)
	if err != nil {
		return credentialDestination{}, err
	}
	destination := credentialDestination{protocol: strings.ToLower(endpoint.Protocol), host: strings.ToLower(endpoint.Host), port: endpoint.Port}
	if destination.port == 0 {
		destination.port = map[string]int{"http": 80, "https": 443, "ssh": 22}[destination.protocol]
	}
	if destination.protocol == "ssh" {
		destination.sshUser = endpoint.User
		if destination.sshUser == "" {
			destination.sshUser = "git"
		}
	}
	return destination, nil
}

func credentialsNeedRebinding(existing *model.GitRepository, input Input) bool {
	if existing == nil {
		return false
	}
	before, beforeErr := repositoryCredentialDestination(existing.CloneURL)
	after, afterErr := repositoryCredentialDestination(input.CloneURL)
	return beforeErr != nil || afterErr != nil || before != after || existing.Provider != input.Provider
}

func validateCredentialDestination(existing *model.GitRepository, input Input) error {
	if !credentialsNeedRebinding(existing, input) {
		return nil
	}
	// 隐藏凭据只授权给原目的地，改变地址后必须明确重新选择或提供凭据。
	if input.AuthType != model.GitAuthNone && (existing.CredentialID != nil || existing.CredentialCiphertext != "") && input.CredentialID == nil && input.Credential == nil {
		return ErrCredentialRebindRequired
	}
	if input.Provider != model.GitProviderGeneric && existing.APICredentialID != nil && input.APICredentialID == nil {
		return ErrCredentialRebindRequired
	}
	return nil
}
