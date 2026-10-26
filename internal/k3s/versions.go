package k3s

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"edo/internal/kube"
	"edo/internal/model"
)

var releaseVersion = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)\+k3s([1-9][0-9]*)$`)

type k3sVersion struct{ major, minor, patch, revision int }

func parseK3sVersion(value string) (k3sVersion, bool) {
	parts := releaseVersion.FindStringSubmatch(value)
	if len(parts) != 5 {
		return k3sVersion{}, false
	}
	numbers := make([]int, 4)
	for i := range numbers {
		number, err := strconv.ParseUint(parts[i+1], 10, 31)
		if err != nil {
			return k3sVersion{}, false
		}
		numbers[i] = int(number)
	}
	return k3sVersion{numbers[0], numbers[1], numbers[2], numbers[3]}, true
}

// TargetVersion is an authorized, scoped live version read for the options UI.
func (s *Service) TargetVersion(ctx context.Context, actorID, clusterID string) (string, error) {
	if _, err := s.authorize(ctx, actorID, "join"); err != nil {
		return "", err
	}
	return s.liveTargetVersion(ctx, strings.TrimSpace(clusterID))
}

func (s *Service) liveTargetVersion(ctx context.Context, clusterID string) (string, error) {
	cluster, err := s.kubernetes.Find(ctx, clusterID)
	if err != nil {
		return "", err
	}
	if !cluster.IsActive {
		return "", kube.ErrClusterNotFound
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	version, err := s.kubernetes.Ping(readCtx, clusterID)
	if err != nil {
		return "", err
	}
	if _, ok := parseK3sVersion(version); !ok {
		return "", kube.ErrJoinUnsupported
	}
	return version, nil
}

func selectInstallerVersion(input *Input) error {
	version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
	version = strings.TrimSpace(version)
	channel, _ := input.Installer["INSTALL_K3S_CHANNEL"].(string)
	channel = strings.TrimSpace(channel)
	if input.Action == "join" {
		if version == "" && channel != "" {
			return &ValidationError{Message: "添加节点不能仅指定发布通道，请填写精确 K3s 版本"}
		}
		if version == "" {
			version = input.targetVersion
		}
		if err := validateJoinVersion(version, input.targetVersion); err != nil {
			return err
		}
		// The exact version takes precedence in the official installer. Remove
		// channel selection so the reviewed join cannot follow a moving release.
		delete(input.Installer, "INSTALL_K3S_CHANNEL")
		delete(input.Installer, "INSTALL_K3S_CHANNEL_URL")
	} else if version == "" && channel == "" {
		version = OptionsCatalog().Version
	}
	if version != "" {
		input.Installer["INSTALL_K3S_VERSION"] = version
	}
	if preloadedBinary(input.Installer) && version == "" {
		return &ValidationError{Message: "使用预装二进制时需要指定准确的 K3s 版本"}
	}
	return nil
}

// K3s requires agents not newer than servers, and delegates minor skew to
// Kubernetes (three minors; agents before 1.25 support only two):
// https://docs.k3s.io/faq#how-compatible-are-different-versions-of-k3s
// https://kubernetes.io/releases/version-skew-policy/#kubelet
func validateJoinVersion(agent, target string) error {
	a, aok := parseK3sVersion(agent)
	t, tok := parseK3sVersion(target)
	if !aok || !tok {
		return &ValidationError{Message: "添加节点需要精确的稳定 K3s 版本，例如 v1.35.5+k3s1"}
	}
	maxSkew := 3
	if a.major == 1 && a.minor < 25 {
		maxSkew = 2
	}
	newer := a.minor > t.minor || a.minor == t.minor && (a.patch > t.patch || a.patch == t.patch && a.revision > t.revision)
	if a.major != t.major || newer || t.minor-a.minor > maxSkew {
		return &ValidationError{Message: "节点版本不能高于目标集群，且最多落后 3 个次版本（1.25 之前最多 2 个），请使用目标集群版本或兼容版本"}
	}
	return nil
}

func (s *Service) recheckRuntime(ctx context.Context, record model.K3sInstallation, snapshot savedInput) error {
	if err := s.recheck(ctx, record, snapshot); err != nil {
		return err
	}
	if snapshot.Input.Action != "join" {
		return nil
	}
	if snapshot.TargetVersion == "" {
		return ErrTargetVersionChanged
	}
	version, err := s.liveTargetVersion(ctx, snapshot.Input.ClusterID)
	if err != nil {
		return err
	}
	if version != snapshot.TargetVersion {
		return ErrTargetVersionChanged
	}
	installed, _ := snapshot.Input.Installer["INSTALL_K3S_VERSION"].(string)
	return validateJoinVersion(installed, version)
}
