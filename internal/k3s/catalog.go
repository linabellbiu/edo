package k3s

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const CatalogK3sVersion = "v1.37.1+k3s1"

var ErrInvalidOptions = errors.New("K3s 安装选项无效")

//go:embed catalog.json
var catalogJSON []byte

type Option struct {
	Name                 string   `json:"name"`
	Label                string   `json:"label"`
	Group                string   `json:"group"`
	Type                 string   `json:"type"`
	Default              any      `json:"default,omitempty"`
	Description          string   `json:"description"`
	Choices              []string `json:"choices,omitempty"`
	Aliases              []string `json:"aliases,omitempty"`
	EnvironmentVariables []string `json:"environment_variables,omitempty"`
	Format               string   `json:"format,omitempty"`
	Source               string   `json:"source"`
	Secret               bool     `json:"secret,omitempty"`
	Managed              bool     `json:"managed,omitempty"`
	Hidden               bool     `json:"hidden,omitempty"`
	Deprecated           bool     `json:"deprecated,omitempty"`
	UnsupportedReason    string   `json:"unsupported_reason,omitempty"`
}

type Catalog struct {
	Version        string   `json:"version"`
	DefaultVersion string   `json:"default_version"`
	TargetVersion  string   `json:"target_version,omitempty"`
	CatalogVersion string   `json:"catalog_version"`
	Sources        []string `json:"sources"`
	Server         []Option `json:"server"`
	Agent          []Option `json:"agent"`
	Installer      []Option `json:"installer"`
}

// OptionsCatalog returns independent data so caller changes cannot corrupt future plans.
func OptionsCatalog() Catalog {
	var catalog Catalog
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		panic("invalid embedded K3s option catalogue")
	}
	catalog.DefaultVersion = catalog.Version
	return catalog
}

func roleOptions(role string) ([]Option, error) {
	catalog := OptionsCatalog()
	switch role {
	case "server":
		return catalog.Server, nil
	case "agent":
		return catalog.Agent, nil
	case "installer":
		return catalog.Installer, nil
	default:
		return nil, fmt.Errorf("%w: 节点角色不正确", ErrInvalidOptions)
	}
}

func indexOptions(options []Option) map[string]Option {
	index := make(map[string]Option, len(options))
	for _, option := range options {
		index[option.Name] = option
		for _, alias := range option.Aliases {
			index[alias] = option
		}
	}
	return index
}

var runtimeOptionName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
var installerOptionName = regexp.MustCompile(`^INSTALL_K3S_[A-Z0-9_]+$`)

// ValidateAndMergeOptions follows K3s replacement semantics: overrides replace a
// selected value (including false and empty lists); YAML null restores the default
// by omitting that key. Unknown runtime flags are accepted only in the YAML editor
// as a bounded, scalar extension for explicitly selected K3s versions.
func ValidateAndMergeOptions(role string, selected map[string]any, overrides string) (map[string]any, error) {
	if len(selected) > 256 {
		return nil, fmt.Errorf("%w: 参数不能超过 256 项", ErrInvalidOptions)
	}
	options, err := roleOptions(role)
	if err != nil {
		return nil, err
	}
	if role == "installer" {
		return nil, fmt.Errorf("%w: 安装器参数需单独校验", ErrInvalidOptions)
	}
	index := indexOptions(options)
	merged := make(map[string]any, len(selected))
	for key, value := range selected {
		option, known := index[key]
		if !known {
			return nil, optionError(key, "不在当前版本选项目录中，请使用覆盖配置")
		}
		if _, exists := merged[option.Name]; exists {
			return nil, optionError(key, "同一选项不能重复设置")
		}
		if err := checkApplicable(option, value); err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		normalized, err := normalizeOption(option, value)
		if err != nil {
			return nil, err
		}
		merged[option.Name] = normalized
	}
	overrideValues, err := DecodeOptionOverrides(overrides)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(overrideValues))
	for key, value := range overrideValues {
		option, known := index[key]
		canonical := key
		if known {
			canonical = option.Name
			if err := checkApplicable(option, value); err != nil {
				return nil, err
			}
		} else if !runtimeOptionName.MatchString(key) || len(key) > 100 {
			return nil, optionError(key, "参数名只能使用小写字母、数字和连字符")
		}
		if seen[canonical] {
			return nil, optionError(key, "同一选项不能重复设置")
		}
		seen[canonical] = true
		if value == nil {
			delete(merged, canonical)
			continue
		}
		var normalized any
		if known {
			normalized, err = normalizeOption(option, value)
		} else {
			normalized, err = normalizeExtension(key, value)
		}
		if err != nil {
			return nil, err
		}
		merged[canonical] = normalized
	}
	if len(merged) > 256 {
		return nil, fmt.Errorf("%w: 参数不能超过 256 项", ErrInvalidOptions)
	}
	return merged, nil
}

// DecodeOptionOverrides rejects multiple documents, duplicate/merge keys, aliases
// and nested maps. Error text deliberately omits YAML values, which may be secrets.
func DecodeOptionOverrides(input string) (map[string]any, error) {
	if len(input) > 64*1024 || !utf8.ValidString(input) || strings.ContainsRune(input, '\x00') {
		return nil, fmt.Errorf("%w: 覆盖配置过长或包含无效字符", ErrInvalidOptions)
	}
	if strings.TrimSpace(input) == "" {
		return map[string]any{}, nil
	}
	decoder := yaml.NewDecoder(strings.NewReader(input))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: 覆盖配置必须是 YAML 参数映射", ErrInvalidOptions)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: 覆盖配置只能包含一个 YAML 文档", ErrInvalidOptions)
	}
	root := document.Content[0]
	if root.Anchor != "" || len(root.Content) > 512 {
		return nil, fmt.Errorf("%w: 参数不能超过 256 项", ErrInvalidOptions)
	}
	values := make(map[string]any, len(root.Content)/2)
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || len(key.Value) > 100 {
			return nil, fmt.Errorf("%w: 覆盖配置的参数名不正确", ErrInvalidOptions)
		}
		if _, exists := values[key.Value]; exists {
			return nil, optionError(key.Value, "同一选项不能重复设置")
		}
		if err := validateYAMLValue(value); err != nil {
			return nil, optionError(key.Value, "只支持标量或同类型标量列表")
		}
		var decoded any
		if err := value.Decode(&decoded); err != nil {
			return nil, optionError(key.Value, "参数值格式不正确")
		}
		values[key.Value] = decoded
	}
	return values, nil
}

func validateYAMLValue(node *yaml.Node) error {
	if node.Anchor != "" {
		return ErrInvalidOptions
	}
	if node.Kind == yaml.ScalarNode {
		switch node.Tag {
		case "!!str", "!!int", "!!float", "!!bool", "!!null":
			return nil
		}
	}
	if node.Kind == yaml.SequenceNode && len(node.Content) <= 256 {
		kind := ""
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode || child.Tag == "!!null" || child.Anchor != "" {
				return ErrInvalidOptions
			}
			if err := validateYAMLValue(child); err != nil {
				return err
			}
			current := child.Tag
			if current == "!!float" || current == "!!int" {
				current = "number"
			}
			if kind != "" && current != kind {
				return ErrInvalidOptions
			}
			kind = current
		}
		return nil
	}
	return ErrInvalidOptions
}

func checkApplicable(option Option, value any) error {
	if option.Managed {
		return optionError(option.Name, option.UnsupportedReason)
	}
	if option.UnsupportedReason != "" {
		if off, ok := value.(bool); ok && !off {
			return nil
		}
		if value == nil {
			return nil
		}
		return optionError(option.Name, option.UnsupportedReason)
	}
	return nil
}

func normalizeOption(option Option, value any) (any, error) {
	var normalized any
	switch option.Type {
	case "boolean":
		v, ok := value.(bool)
		if !ok {
			return nil, optionError(option.Name, "需要布尔值")
		}
		normalized = v
	case "integer":
		v, ok := integerValue(value)
		if !ok || v < 0 || v > math.MaxInt32 {
			return nil, optionError(option.Name, "需要 0 至 2147483647 的整数")
		}
		if strings.Contains(option.Name, "port") && v > 65535 {
			return nil, optionError(option.Name, "端口不能超过 65535")
		}
		normalized = v
	case "string[]":
		var values []string
		switch v := value.(type) {
		case string:
			values = []string{v}
		case []string:
			values = append([]string{}, v...)
		case []any:
			values = make([]string, 0, len(v))
			for _, item := range v {
				text, ok := item.(string)
				if !ok {
					return nil, optionError(option.Name, "需要字符串列表")
				}
				values = append(values, text)
			}
		default:
			return nil, optionError(option.Name, "需要字符串列表")
		}
		if len(values) > 256 {
			return nil, optionError(option.Name, "列表不能超过 256 项")
		}
		for _, text := range values {
			if err := validOptionString(text); err != nil {
				return nil, optionError(option.Name, "字符串过长或包含无效字符")
			}
		}
		normalized = values
	default:
		text, ok := value.(string)
		if !ok {
			return nil, optionError(option.Name, "需要字符串")
		}
		if err := validOptionString(text); err != nil {
			return nil, optionError(option.Name, "字符串过长或包含无效字符")
		}
		if option.Format == "duration" {
			if duration, err := time.ParseDuration(text); err != nil || duration < 0 {
				return nil, optionError(option.Name, "需要时间长度，例如 5m")
			}
		}
		if len(option.Choices) > 0 && text != "" {
			found := false
			for _, choice := range option.Choices {
				found = found || text == choice
			}
			if !found {
				return nil, optionError(option.Name, "不在可选值中")
			}
		}
		normalized = text
	}
	return normalized, nil
}

func integerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case uint64:
		if number <= math.MaxInt64 {
			return int64(number), true
		}
	case float64:
		if !math.IsNaN(number) && !math.IsInf(number, 0) && math.Trunc(number) == number && number >= math.MinInt32 && number <= math.MaxInt32 {
			return int64(number), true
		}
	case json.Number:
		v, err := number.Int64()
		return v, err == nil
	}
	return 0, false
}

func validOptionString(value string) error {
	if len(value) > 16*1024 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return ErrInvalidOptions
	}
	return nil
}

func normalizeExtension(key string, value any) (any, error) {
	if err := validExtensionValue(value); err != nil {
		return nil, optionError(key, "参数值格式不正确")
	}
	data, err := yaml.Marshal(value)
	if err != nil || len(data) > 16*1024 {
		return nil, optionError(key, "参数值过长或格式不正确")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil || len(node.Content) != 1 || validateYAMLValue(node.Content[0]) != nil {
		return nil, optionError(key, "只支持标量或同类型标量列表")
	}
	if bytes.ContainsRune(data, '\x00') {
		return nil, optionError(key, "参数值包含无效字符")
	}
	return value, nil
}

func validExtensionValue(value any) error {
	switch data := value.(type) {
	case string:
		return validOptionString(data)
	case bool, int, int64, uint64, json.Number:
		return nil
	case float64:
		if !math.IsNaN(data) && !math.IsInf(data, 0) {
			return nil
		}
	case []any:
		for _, item := range data {
			if err := validExtensionValue(item); err != nil {
				return err
			}
		}
		return nil
	case []string:
		for _, item := range data {
			if err := validOptionString(item); err != nil {
				return err
			}
		}
		return nil
	}
	return ErrInvalidOptions
}

func optionError(name, detail string) error {
	if !runtimeOptionName.MatchString(name) && !installerOptionName.MatchString(name) {
		return fmt.Errorf("%w: 参数名不正确", ErrInvalidOptions)
	}
	return fmt.Errorf("%w: %s：%s", ErrInvalidOptions, name, detail)
}

func OptionWarnings(role string, values map[string]any) []string {
	options, err := roleOptions(role)
	if err != nil {
		return nil
	}
	index := indexOptions(options)
	var warnings []string
	for key := range values {
		if _, exists := index[key]; !exists {
			warnings = append(warnings, fmt.Sprintf("%s 不在 %s 选项目录中，将由所选 K3s 版本验证", key, CatalogK3sVersion))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// ValidateInstallerOptions accepts official environment names and form aliases
// (version, channel, bin-dir, ...). Its output always uses official names.
func ValidateInstallerOptions(selected map[string]any) (map[string]any, error) {
	if len(selected) > 32 {
		return nil, fmt.Errorf("%w: 安装器参数过多", ErrInvalidOptions)
	}
	index := indexOptions(OptionsCatalog().Installer)
	result := make(map[string]any, len(selected))
	for key, value := range selected {
		name := key
		if !strings.HasPrefix(name, "INSTALL_K3S_") {
			name = "INSTALL_K3S_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		}
		option, known := index[name]
		if !known {
			return nil, optionError(key, "不在安装器选项目录中")
		}
		if _, duplicate := result[name]; duplicate {
			return nil, optionError(name, "同一选项不能重复设置")
		}
		if err := checkApplicable(option, value); err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		normalized, err := normalizeOption(option, value)
		if err != nil {
			return nil, err
		}
		if text, ok := normalized.(string); ok {
			if strings.ContainsAny(text, "\r\n") {
				return nil, optionError(name, "不能包含换行")
			}
			switch name {
			case "INSTALL_K3S_NAME":
				if text != "" && (!runtimeOptionName.MatchString(text) || len(text) > 63) {
					return nil, optionError(name, "服务名称只能使用小写字母、数字和连字符")
				}
			case "INSTALL_K3S_BIN_DIR", "INSTALL_K3S_SYSTEMD_DIR":
				if text == "/" || !path.IsAbs(text) || path.Clean(text) != text {
					return nil, optionError(name, "需要规范的 Linux 绝对目录")
				}
			}
		}
		result[name] = normalized
	}
	if len(result) > 32 {
		return nil, fmt.Errorf("%w: 安装器参数过多", ErrInvalidOptions)
	}
	return result, nil
}

// ValidateAndMergeInstallerOptions makes aliases and official environment names
// refer to the same field before applying the final YAML override.
func ValidateAndMergeInstallerOptions(selected map[string]any, overrides string) (map[string]any, error) {
	result, err := ValidateInstallerOptions(selected)
	if err != nil {
		return nil, err
	}
	values, err := DecodeOptionOverrides(overrides)
	if err != nil {
		return nil, err
	}
	index := indexOptions(OptionsCatalog().Installer)
	seen := map[string]bool{}
	for key, value := range values {
		name := key
		if !strings.HasPrefix(name, "INSTALL_K3S_") {
			name = "INSTALL_K3S_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		}
		option, known := index[name]
		if !known {
			return nil, optionError(key, "不在安装器选项目录中")
		}
		if seen[name] {
			return nil, optionError(name, "同一选项不能重复设置")
		}
		seen[name] = true
		if err := checkApplicable(option, value); err != nil {
			return nil, err
		}
		if value == nil {
			delete(result, name)
			continue
		}
		normalized, err := ValidateInstallerOptions(map[string]any{name: value})
		if err != nil {
			return nil, err
		}
		result[name] = normalized[name]
	}
	return result, nil
}

var secretOptionName = regexp.MustCompile(`(?i)(token|secret|password|passwd|credential|private.?key|access.?key|api[._-]?key|keyfile|auth|datastore.endpoint)`)
var secretArgument = regexp.MustCompile(`(?i)(token|secret|password|passwd|credential|private.?key|access.?key|api[._-]?key|keyfile|auth[a-z0-9._-]*)\s*[:=]`)
var authorizationValue = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[^\s,;]+`)
var authenticatedURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s/]*@`)

func IsSecretOption(name string) bool {
	// These describe an encryption policy, not credential material. Hiding false
	// or the provider name would make the reviewed installation plan inaccurate.
	if name == "secrets-encryption" || name == "secrets-encryption-provider" {
		return false
	}
	return secretOptionName.MatchString(name)
}

// RedactOptions also covers future keys and nested values in diagnostic data.
func RedactOptions(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		if IsSecretOption(key) {
			result[key] = "[已隐藏]"
			continue
		}
		result[key] = redactValue(value)
	}
	return result
}

func redactValue(value any) any {
	switch data := value.(type) {
	case string:
		if secretArgument.MatchString(data) || authorizationValue.MatchString(data) || authenticatedURL.MatchString(data) {
			return "[已隐藏]"
		}
		if parsed, err := url.Parse(data); err == nil && parsed.User != nil {
			return "[已隐藏]"
		}
		return data
	case map[string]any:
		return RedactOptions(data)
	case []string:
		result := make([]string, len(data))
		for i, item := range data {
			result[i] = redactValue(item).(string)
		}
		return result
	case []any:
		result := make([]any, len(data))
		for i, item := range data {
			result[i] = redactValue(item)
		}
		return result
	default:
		return value
	}
}
