//go:build mage

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"go.yaml.in/yaml/v3"
	"mvdan.cc/sh/v3/syntax"
)

type ciContractStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

type ciContractJob struct {
	Timeout  int `yaml:"timeout-minutes"`
	Strategy struct {
		Matrix map[string][]string `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []ciContractStep `yaml:"steps"`
}

func ciContractJobs(t *testing.T) map[string]ciContractJob {
	t.Helper()
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]ciContractJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil || len(workflow.Jobs) == 0 {
		t.Fatalf("解析 CI 工作流失败: %v", err)
	}
	return workflow.Jobs
}

func ciContractTestFunctions(t *testing.T, directory string, include func(*ast.FuncDecl) bool) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && strings.HasPrefix(function.Name.Name, "Test") && include(function) {
				names = append(names, function.Name.Name)
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("没有找到需要验证的真实测试函数: %s", directory)
	}
	sort.Strings(names)
	return names
}

func ciContractIntegrationGate(function *ast.FuncDecl, variable string) bool {
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Getenv" {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		literal, isLiteral := call.Args[0].(*ast.BasicLit)
		if ok && owner.Name == "os" && isLiteral {
			value, err := strconv.Unquote(literal.Value)
			found = found || err == nil && value == variable
		}
		return true
	})
	return found
}

func ciContractLiteral(parts []syntax.WordPart) (string, bool) {
	var result strings.Builder
	for _, part := range parts {
		switch value := part.(type) {
		case *syntax.Lit:
			result.WriteString(value.Value)
		case *syntax.SglQuoted:
			if value.Dollar {
				return "", false
			}
			result.WriteString(value.Value)
		case *syntax.DblQuoted:
			text, literal := ciContractLiteral(value.Parts)
			if value.Dollar || !literal {
				return "", false
			}
			result.WriteString(text)
		default:
			return "", false
		}
	}
	return result.String(), true
}

type ciContractGoCommand struct {
	Arguments []string
	Env       map[string]string
}

func ciContractGoTests(t *testing.T, job ciContractJob) []ciContractGoCommand {
	t.Helper()
	var commands []ciContractGoCommand
	for _, step := range job.Steps {
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(step.Run), step.Name)
		if err != nil {
			t.Fatalf("CI Shell 语法无效: step=%s err=%v", step.Name, err)
		}
		syntax.Walk(file, func(node syntax.Node) bool {
			call, ok := node.(*syntax.CallExpr)
			if !ok || len(call.Args) < 2 || call.Args[0].Lit() != "go" || call.Args[1].Lit() != "test" {
				return true
			}
			arguments := make([]string, 0, len(call.Args)-2)
			for _, word := range call.Args[2:] {
				text, _ := ciContractLiteral(word.Parts)
				arguments = append(arguments, text)
			}
			commands = append(commands, ciContractGoCommand{Arguments: arguments, Env: step.Env})
			return true
		})
	}
	return commands
}

func ciContractFlag(arguments []string, name string) string {
	for index, argument := range arguments {
		if value, found := strings.CutPrefix(argument, name+"="); found {
			return value
		}
		if argument == name && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}

func ciContractBounded(t *testing.T, arguments []string, jobTimeout int) {
	t.Helper()
	race := false
	for _, argument := range arguments {
		race = race || argument == "-race"
	}
	duration, err := time.ParseDuration(ciContractFlag(arguments, "-timeout"))
	if !race || ciContractFlag(arguments, "-count") != "1" || err != nil || duration <= 0 || jobTimeout <= 0 || duration >= time.Duration(jobTimeout)*time.Minute {
		t.Fatalf("真实集成测试必须保留竞争检测、禁用结果缓存及两层有界超时: args=%v job_timeout=%d", arguments, jobTimeout)
	}
}

func TestCIWorkflowSelectorsCoverRealIntegrationTests(t *testing.T) {
	jobs := ciContractJobs(t)
	type suite struct {
		job, directory, gate string
		include              func(*ast.FuncDecl) bool
	}
	suites := []suite{
		{"database", "internal/database", "", func(function *ast.FuncDecl) bool {
			return strings.HasPrefix(function.Name.Name, "TestExternal")
		}},
	}
	// 从整个后端扫描实际门控，新增其他包的真实 Docker 测试也不能悄悄漏跑。
	gated := map[[2]string]bool{}
	if err := filepath.WalkDir("internal", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(function.Name.Name, "Test") {
				continue
			}
			for _, gate := range []string{"EDO_TEST_DOCKER_INTEGRATION", "EDO_TEST_DOCKER_STOP"} {
				if ciContractIntegrationGate(function, gate) {
					gated[[2]string{filepath.ToSlash(filepath.Dir(path)), gate}] = true
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for key := range gated {
		gate := key[1]
		suites = append(suites, suite{"docker", key[0], gate, func(function *ast.FuncDecl) bool {
			return ciContractIntegrationGate(function, gate)
		}})
	}
	for _, item := range suites {
		t.Run(item.job+"/"+item.directory, func(t *testing.T) {
			job := jobs[item.job]
			commands := ciContractGoTests(t, job)
			for _, command := range commands {
				ciContractBounded(t, command.Arguments, job.Timeout)
			}
			for _, name := range ciContractTestFunctions(t, item.directory, item.include) {
				covered := false
				for _, command := range commands {
					arguments := command.Arguments
					selector := ciContractFlag(arguments, "-run")
					if selector == "" || !slices.Contains(arguments, "./"+item.directory) || item.gate != "" && command.Env[item.gate] != "1" {
						continue
					}
					pattern, err := regexp.Compile(selector)
					if err != nil {
						t.Fatal(err)
					}
					covered = covered || pattern.MatchString(name)
				}
				if !covered {
					t.Errorf("CI selector 遗漏真实集成测试: job=%s test=%s", item.job, name)
				}
			}
		})
	}
}

func ciContractIdentityDatabase(t *testing.T, driver, dsn string) string {
	t.Helper()
	if driver == "postgres" {
		connection, err := pgconn.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if connection.Host != "127.0.0.1" || connection.Port != 5432 || connection.User != "edo" || connection.Password != "edo_test" || connection.RuntimeParams["TimeZone"] != "Asia/Shanghai" {
			t.Fatal("PostgreSQL 身份测试连接未使用临时服务的正确用户、端口及时区")
		}
		return connection.Database
	}
	connection, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Addr != "127.0.0.1:3306" || connection.User != "root" || connection.Passwd != "edo_test" || !connection.ParseTime || connection.Loc.String() != "Asia/Shanghai" {
		t.Fatal("MySQL 身份测试连接未使用临时服务的正确用户、端口及时区")
	}
	return connection.DBName
}

func TestCIIdentityModesUseDedicatedEmptyDatabases(t *testing.T) {
	job := ciContractJobs(t)["database"]
	drivers := slices.Clone(job.Strategy.Matrix["driver"])
	sort.Strings(drivers)
	if strings.Join(drivers, ",") != "mysql,postgres,sqlite" {
		t.Fatal("数据库矩阵必须保留 SQLite、PostgreSQL 与 MySQL")
	}
	var identityStep *ciContractStep
	for index := range job.Steps {
		if job.Steps[index].Env["EDO_ID_TEST_DRIVER"] != "" {
			if identityStep != nil {
				t.Fatal("身份测试不应重复运行")
			}
			identityStep = &job.Steps[index]
		}
	}
	if identityStep == nil || identityStep.If != "${{ matrix.driver != 'sqlite' }}" {
		t.Fatal("独立身份空库步骤必须只在外部数据库分片执行")
	}
	for key, value := range map[string]string{
		"EDO_ID_TEST_DRIVER": "${{ matrix.driver }}", "POSTGRES_SERVICE_ID": "${{ job.services.postgres.id }}", "MYSQL_SERVICE_ID": "${{ job.services.mysql.id }}",
	} {
		if identityStep.Env[key] != value {
			t.Fatalf("身份测试步骤引用了错误的矩阵或临时服务: key=%s", key)
		}
	}
	names := ciContractTestFunctions(t, "internal/database", func(function *ast.FuncDecl) bool {
		migrates := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if ok {
				name, direct := call.Fun.(*ast.Ident)
				migrates = migrates || direct && name.Name == "Migrate"
			}
			return true
		})
		return strings.HasPrefix(function.Name.Name, "TestRowIdentit") && migrates
	})
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range []string{"postgres", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			directory := t.TempDir()
			goLog, dockerLog := filepath.Join(directory, "go.log"), filepath.Join(directory, "docker.log")
			// 只演练工作流的空库分配和命令选择，不启动 Docker，也不冒充数据库兼容性验证。
			for name, source := range map[string]string{
				"go":     "#!/bin/sh\nprintf '%s\\t%s\\t%s\\n' \"$EDO_ID_TEST_DRIVER\" \"$EDO_ID_TEST_DSN\" \"$*\" >> \"$EDO_CI_GO_LOG\"\n",
				"docker": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$EDO_CI_DOCKER_LOG\"\n",
			} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", identityStep.Run)
			command.Dir = directory
			command.Env = []string{
				"PATH=" + directory + ":/usr/bin:/bin", "EDO_ID_TEST_DRIVER=" + driver,
				"POSTGRES_SERVICE_ID=postgres-container", "MYSQL_SERVICE_ID=mysql-container",
				"EDO_CI_GO_LOG=" + goLog, "EDO_CI_DOCKER_LOG=" + dockerLog,
			}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("身份空库步骤无法执行: err=%v output=%s", err, output)
			}
			data, err := os.ReadFile(goLog)
			if err != nil {
				t.Fatal(err)
			}
			creation, err := os.ReadFile(dockerLog)
			if err != nil {
				t.Fatal(err)
			}
			created := strings.Split(strings.TrimSpace(string(creation)), "\n")
			seenTests, seenDatabases := map[string]bool{}, map[string]bool{}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				fields := strings.SplitN(line, "\t", 3)
				if len(fields) != 3 || fields[0] != driver {
					t.Fatal("身份测试缺少明确的驱动或连接参数")
				}
				arguments := strings.Fields(fields[2])
				if len(arguments) == 0 || arguments[0] != "test" || !slices.Contains(arguments, "./internal/database") {
					t.Fatal("身份模式没有执行真实数据库测试包")
				}
				ciContractBounded(t, arguments, job.Timeout)
				selector := ciContractFlag(arguments, "-run")
				name := strings.TrimSuffix(strings.TrimPrefix(selector, "^"), "$")
				if selector != "^"+name+"$" || seenTests[name] {
					t.Fatal("每种身份模式必须精确运行一次")
				}
				database := ciContractIdentityDatabase(t, driver, fields[1])
				if !strings.HasPrefix(database, "edo_identity_") || seenDatabases[database] {
					t.Fatal("身份模式复用了已有或其他模式的数据库")
				}
				found := false
				for _, call := range created {
					createsDatabase := strings.Contains(call, " createdb ") && strings.HasSuffix(call, " "+database) || strings.Contains(call, " mysql ") && strings.Contains(call, "--execute=CREATE DATABASE "+database+" ")
					found = found || strings.Contains(call, driver+"-container") && createsDatabase
				}
				if !found {
					t.Fatal("身份模式没有通过当前临时服务创建自己的空库")
				}
				seenTests[name], seenDatabases[database] = true, true
			}
			if len(created) != len(names) || len(seenTests) != len(names) {
				t.Fatalf("身份空库或测试调用次数错误: create=%d tests=%d want=%d", len(created), len(seenTests), len(names))
			}
			for _, name := range names {
				if !seenTests[name] {
					t.Errorf("CI 遗漏身份模式: %s", name)
				}
			}
		})
	}
}
