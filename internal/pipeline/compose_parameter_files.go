package pipeline

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"edo/internal/configfile"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

const maximumComposeParameterFileBytes = 1024 * 1024

// 只准备参数明确引用的文件，使用本次固定 Commit，既不复制整个仓库也不把正文放入快照。
func (s *Service) prepareComposeParameterFiles(ctx context.Context, prepared *executionContext) error {
	if prepared.deploymentPlan.Kind != model.DeploymentPlanComposeStack || strings.TrimSpace(prepared.node.Config.DeploymentScript) != "" {
		return nil
	}
	parameters, err := configfile.ParseEnvFileParameters(prepared.node.Config.DeploymentParameters, prepared.node.Config.ConfigurationFiles)
	if err != nil {
		return err
	}
	if len(parameters) == 0 {
		return nil
	}
	fail := func(operation string, err error) error {
		s.logger.WarnContext(ctx, "准备 Compose 参数文件失败", "operation", operation, "run_id", prepared.run.ID, "node_id", prepared.node.ID, "err", err)
		return errors.New("无法准备 --env-file 文件，请检查本次代码中的相对路径及文件格式")
	}
	var root *os.Root
	for _, parameter := range parameters {
		if parameter.Configuration {
			continue
		}
		if prepared.component.RepositoryID != "" && prepared.run.CommitSHA != "" {
			if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
				return err
			}
			lease, err := s.repositories.AcquireTaskCheckout(ctx, prepared.component.RepositoryID, pipelineRunCheckoutRef(prepared.run), prepared.run.CommitSHA, prepared.run.ID+":"+prepared.node.ID+":compose-env")
			if err != nil {
				return fail("compose_parameter_checkout", err)
			}
			defer lease.Release()
			root, err = os.OpenRoot(lease.Directory)
			if err != nil {
				return fail("compose_parameter_root", err)
			}
			defer root.Close()
		}
		break
	}
	prepared.composeEnvValues = map[string]string{}
	staged := map[string]bool{}
	for _, parameter := range parameters {
		var content string
		if parameter.Configuration {
			if prepared.configuration == nil {
				continue
			}
			var exists bool
			content, exists = prepared.configuration.Files[parameter.Path]
			if !exists {
				continue
			}
		} else {
			if root == nil {
				continue
			}
			body, exists, err := readComposeParameterFile(root, parameter.Path)
			if err != nil {
				return fail("compose_parameter_read", err)
			}
			if !exists {
				// 仓库没有此文件时由 Compose 检查执行主机工作目录，保存不要求文件预先存在。
				continue
			}
			content = string(body)
			if !staged[parameter.Path] {
				if len(prepared.inputFiles) >= 32 {
					return fail("compose_parameter_count", ErrDeploymentInputs)
				}
				prepared.inputFiles = append(prepared.inputFiles, sshdeploy.FileInput{Directory: prepared.target.WorkingDirectory, Path: parameter.Path, Digest: sha256Digest(body), Content: strings.NewReader(content)})
				staged[parameter.Path] = true
			}
		}
		// 复用 Compose 官方 dotenv 语法与覆盖顺序，不加载到 EDO 进程环境。
		values, err := dotenv.ParseWithLookup(strings.NewReader(content), func(name string) (string, bool) {
			if value, ok := prepared.variables[name]; ok {
				return value, true
			}
			value, ok := prepared.composeEnvValues[name]
			return value, ok
		})
		if err != nil {
			// dotenv 的原始诊断可能包含密钥片段，仅记录稳定的解析失败分类。
			return fail("compose_parameter_parse", errors.New("ENV 文件语法无效"))
		}
		maps.Copy(prepared.composeEnvValues, values)
		prepared.variableRedactions = append(prepared.variableRedactions, content)
		prepared.variableRedactions = append(prepared.variableRedactions, strings.Split(content, "\n")...)
		for _, value := range values {
			if value != "" {
				prepared.variableRedactions = append(prepared.variableRedactions, value)
			}
		}
	}
	return nil
}

func readComposeParameterFile(root *os.Root, name string) ([]byte, bool, error) {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || i == len(parts)-1 && (!info.Mode().IsRegular() || info.Size() > maximumComposeParameterFileBytes) {
			return nil, false, ErrDeploymentInputs
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximumComposeParameterFileBytes+1))
	if err != nil || len(content) > maximumComposeParameterFileBytes || strings.ContainsRune(string(content), '\x00') {
		return nil, false, ErrDeploymentInputs
	}
	return content, true, nil
}

func composeInterpolationValues(prepared *executionContext) map[string]string {
	values := map[string]string{}
	if prepared.configuration != nil {
		maps.Copy(values, prepared.configuration.Values)
	}
	maps.Copy(values, prepared.composeEnvValues)
	maps.Copy(values, prepared.variables)
	return values
}

func composeParameterConfigurationPrefix(prepared *executionContext) (string, error) {
	if prepared.configuration != nil {
		return "", nil
	}
	parameters, err := configfile.ParseEnvFileParameters(prepared.node.Config.DeploymentParameters, prepared.node.Config.ConfigurationFiles)
	if err != nil {
		return "", err
	}
	for _, parameter := range parameters {
		if parameter.Configuration {
			return configfile.WriteScript(prepared.snapshot.ApplicationConfigDirectory, nil)
		}
	}
	return "", nil
}
