package dockerengine

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const (
	dockerImagePlaceholder         = "${EDO_IMAGE}"
	dockerContainerNamePlaceholder = "${EDO_CONTAINER_NAME}"
)

type dockerRunTemplate struct {
	arguments  []string
	imageIndex int
	hasName    bool
	hasDetach  bool
}

func parseDockerRunTemplate(value string) (dockerRunTemplate, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(value), "docker-run")
	if err != nil || len(file.Stmts) != 1 || len(file.Last) != 0 {
		return dockerRunTemplate{}, ErrInvalidContainerConfig
	}
	statement := file.Stmts[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Negated || statement.Background || statement.Coprocess || statement.Disown ||
		len(statement.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 3 {
		return dockerRunTemplate{}, ErrInvalidContainerConfig
	}

	arguments := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		argument, wordErr := dockerCommandWord(word)
		if wordErr != nil || argument == "" || strings.ContainsRune(argument, '\x00') {
			return dockerRunTemplate{}, ErrInvalidContainerConfig
		}
		arguments = append(arguments, argument)
	}
	if arguments[0] != "docker" || arguments[1] != "run" {
		return dockerRunTemplate{}, ErrInvalidContainerConfig
	}

	parsed := dockerRunTemplate{arguments: arguments, imageIndex: -1}
	for index := 2; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == dockerImagePlaceholder {
			if parsed.imageIndex >= 0 {
				return dockerRunTemplate{}, ErrInvalidContainerConfig
			}
			parsed.imageIndex = index
			continue
		}
		if strings.Contains(argument, "${EDO_") && argument != dockerContainerNamePlaceholder &&
			argument != "--name="+dockerContainerNamePlaceholder {
			return dockerRunTemplate{}, ErrInvalidContainerConfig
		}
	}
	if parsed.imageIndex < 2 {
		return dockerRunTemplate{}, ErrInvalidContainerConfig
	}
	if err := validateDockerRunOptions(&parsed); err != nil {
		return dockerRunTemplate{}, err
	}
	return parsed, nil
}

func dockerCommandWord(word *syntax.Word) (string, error) {
	if word == nil || len(word.Parts) == 0 {
		return "", ErrInvalidContainerConfig
	}
	var value strings.Builder
	for _, part := range word.Parts {
		if err := appendDockerCommandWordPart(&value, part); err != nil {
			return "", err
		}
	}
	return value.String(), nil
}

func appendDockerCommandWordPart(value *strings.Builder, part syntax.WordPart) error {
	switch typed := part.(type) {
	case *syntax.Lit:
		value.WriteString(typed.Value)
	case *syntax.SglQuoted:
		if typed.Dollar {
			return ErrInvalidContainerConfig
		}
		value.WriteString(typed.Value)
	case *syntax.DblQuoted:
		if typed.Dollar {
			return ErrInvalidContainerConfig
		}
		for _, nested := range typed.Parts {
			if err := appendDockerCommandWordPart(value, nested); err != nil {
				return err
			}
		}
	case *syntax.ParamExp:
		if typed.Param == nil || typed.Flags != nil || typed.NestedParam != nil || typed.Index != nil ||
			typed.Excl || typed.Length || typed.Width || typed.IsSet || typed.Slice != nil || typed.Repl != nil ||
			typed.Names != 0 || typed.Exp != nil || len(typed.Modifiers) != 0 {
			return ErrInvalidContainerConfig
		}
		switch typed.Param.Value {
		case "EDO_IMAGE":
			value.WriteString(dockerImagePlaceholder)
		case "EDO_CONTAINER_NAME":
			value.WriteString(dockerContainerNamePlaceholder)
		default:
			return ErrInvalidContainerConfig
		}
	default:
		return ErrInvalidContainerConfig
	}
	return nil
}

func validateDockerRunOptions(parsed *dockerRunTemplate) error {
	if parsed == nil {
		return ErrInvalidContainerConfig
	}
	arguments := parsed.arguments
	for index := 2; index < parsed.imageIndex; index++ {
		argument := arguments[index]
		switch {
		case argument == "--detach" || argument == "-d" || strings.Contains(strings.TrimPrefix(argument, "-"), "d") &&
			strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--"):
			parsed.hasDetach = true
		case argument == "--name":
			if index+1 >= parsed.imageIndex || arguments[index+1] != dockerContainerNamePlaceholder || parsed.hasName {
				return ErrInvalidContainerConfig
			}
			parsed.hasName = true
			index++
		case strings.HasPrefix(argument, "--name="):
			if strings.TrimPrefix(argument, "--name=") != dockerContainerNamePlaceholder || parsed.hasName {
				return ErrInvalidContainerConfig
			}
			parsed.hasName = true
		case unsafeDockerRunOption(argument):
			return ErrInvalidContainerConfig
		case argument == "--label" || argument == "-l":
			if index+1 >= parsed.imageIndex || strings.HasPrefix(strings.ToLower(arguments[index+1]), "edo.") {
				return ErrInvalidContainerConfig
			}
			index++
		case strings.HasPrefix(argument, "--label=") || strings.HasPrefix(argument, "-l="):
			label := argument[strings.IndexByte(argument, '=')+1:]
			if strings.HasPrefix(strings.ToLower(label), "edo.") {
				return ErrInvalidContainerConfig
			}
		}
	}
	return nil
}

func unsafeDockerRunOption(argument string) bool {
	lower := strings.ToLower(argument)
	unsafePrefixes := []string{
		"--privileged", "--rm", "--pid", "--ipc", "--uts", "--userns", "--cgroupns",
		"--device", "--device-cgroup-rule", "--cap-add", "--security-opt", "--runtime", "--gpus",
		"--sysctl", "--ulimit", "--network", "--net", "--volume", "--volumes-from", "--mount",
		"--env-file", "--add-host",
	}
	for _, prefix := range unsafePrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+"=") {
			return true
		}
	}
	return lower == "-v" || strings.HasPrefix(lower, "-v=") || (strings.HasPrefix(lower, "-v") && len(lower) > 2)
}

func dockerRunCommandArguments(
	template, image, imageDisplay, containerName, targetID, deploymentID string,
) ([]string, error) {
	parsed, err := parseDockerRunTemplate(template)
	if err != nil || strings.TrimSpace(image) == "" || strings.TrimSpace(containerName) == "" ||
		strings.TrimSpace(targetID) == "" || strings.TrimSpace(deploymentID) == "" {
		return nil, ErrInvalidContainerConfig
	}
	arguments := make([]string, 0, len(parsed.arguments)+12)
	for index, argument := range parsed.arguments {
		if index == parsed.imageIndex {
			if !parsed.hasDetach {
				arguments = append(arguments, "--detach")
			}
			if !parsed.hasName {
				arguments = append(arguments, "--name", containerName)
			}
			arguments = append(arguments,
				"--network", defaultDockerNetwork,
				"--label", "edo.managed=true",
				"--label", "edo.deployment.target.id="+targetID,
				"--label", "edo.deployment.id="+deploymentID,
			)
			if imageDisplay = strings.TrimSpace(imageDisplay); imageDisplay != "" {
				arguments = append(arguments, "--label", managedImageDisplayLabel+"="+imageDisplay)
			}
			arguments = append(arguments, image)
			continue
		}
		if argument == dockerContainerNamePlaceholder {
			argument = containerName
		} else if strings.Contains(argument, dockerContainerNamePlaceholder) {
			return nil, ErrInvalidContainerConfig
		}
		arguments = append(arguments, argument)
	}
	if len(arguments) < 3 {
		return nil, ErrInvalidContainerConfig
	}
	return arguments, nil
}

func renderDockerCommand(arguments []string) (string, error) {
	if len(arguments) < 3 || arguments[0] != "docker" || arguments[1] != "run" {
		return "", ErrInvalidContainerConfig
	}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		if argument == "" || strings.ContainsAny(argument, "\x00\r\n") {
			return "", ErrInvalidContainerConfig
		}
		quoted[index] = shellQuote(argument)
	}
	return strings.Join(quoted, " "), nil
}
