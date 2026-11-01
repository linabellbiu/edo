package pipeline

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"edo/internal/model"
)

func TestScriptBuildPlanRejectsUnsafePathsBeforePersistence(t *testing.T) {
	invalid := []struct{ name, value string }{
		{"父目录", "../outside"},
		{"嵌套越界", "nested/../../outside"},
		{"绝对路径", "/outside"},
		{"反斜杠", `dir\file`},
		{"空字符", "bad\x00path"},
		{"超过字节上限", strings.Repeat("a", 513)},
	}
	for _, field := range []string{"working_directory", "artifact_path"} {
		for _, operation := range []string{"create", "update"} {
			for _, tc := range invalid {
				t.Run(field+"/"+operation+"/"+tc.name, func(t *testing.T) {
					service, _ := newPipelineMockService(t)
					input := BuildPlanInput{Name: "脚本路径校验", Kind: model.BuildPlanScript, Script: "true", WorkingDirectory: ".", TimeoutSeconds: 30}
					if field == "working_directory" {
						input.WorkingDirectory = tc.value
					} else {
						input.ArtifactPath = tc.value
					}
					// 不登记任何SQL：无效输入必须在查询/事务/持久写入之前拒绝。
					var plan *model.BuildPlan
					var err error
					if operation == "create" {
						plan, err = service.CreateBuildPlan(context.Background(), "admin", input)
					} else {
						plan, err = service.UpdateBuildPlan(context.Background(), "plan", input)
					}
					if plan != nil || !errors.Is(err, ErrInvalidBuildPlan) {
						t.Fatalf("不安全路径未在数据库前稳定拒绝: plan_non_nil=%t err=%v", plan != nil, err)
					}
				})
			}
		}
	}
}

var safeScriptBuildPlanPaths = []struct {
	name, working, artifact, wantWorking string
}{
	{"默认工作目录且不存产物", "", "", "."},
	{"点目录", ".", ".", "."},
	{"合法嵌套", "src/module", "dist/output.tar", "src/module"},
	{"空格与中文", "./源码 空间", "./产物 空间/result.tar", "./源码 空间"},
	{"清理后仍在工作区", "src/../cmd", "out/../dist/", "src/../cmd"},
	{"字节上限", strings.Repeat("a", 512), strings.Repeat("界", 170) + "ab", strings.Repeat("a", 512)},
}

func TestScriptBuildPlanPreservesSafePathsOnCreate(t *testing.T) {
	for _, tc := range safeScriptBuildPlanPaths {
		t.Run(tc.name, func(t *testing.T) {
			service, mock := newPipelineMockService(t)
			const script = "\n  printf 'ready'  \n"
			args := make([]driver.Value, 26)
			for i := range args {
				args[i] = sqlmock.AnyArg()
			}
			args[5], args[8], args[9] = script, tc.wantWorking, tc.artifact
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "build_plans" \("uuid","name","kind","config_version","description","script","dockerfile_path","context_path","working_directory","artifact_path"`).
				WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			plan, err := service.CreateBuildPlan(context.Background(), "admin", BuildPlanInput{
				Name: "合法路径字节构建", Kind: model.BuildPlanScript, Script: script,
				WorkingDirectory: tc.working, ArtifactPath: tc.artifact, TimeoutSeconds: 30,
			})
			if err != nil || plan == nil {
				t.Fatalf("合法脚本路径未正常提交: err=%v", err)
			}
			if plan.Script != script || plan.WorkingDirectory != tc.wantWorking || plan.ArtifactPath != tc.artifact {
				t.Fatal("合法路径或脚本字节被静默清理改写")
			}
		})
	}
}

func TestScriptBuildPlanPreservesSafePathsOnUpdate(t *testing.T) {
	for _, tc := range safeScriptBuildPlanPaths {
		t.Run(tc.name, func(t *testing.T) {
			service, mock := newPipelineMockService(t)
			const script = "\n  printf 'updated'  \n"
			mock.ExpectQuery(`SELECT .* FROM "build_plans"`).WithArgs("plan", 1).
				WillReturnRows(sqlmock.NewRows([]string{"uuid", "config_version"}).AddRow("plan", 1))
			args := make([]driver.Value, 20)
			for i := range args {
				args[i] = sqlmock.AnyArg()
			}
			args[0], args[14], args[18], args[19] = tc.artifact, script, tc.wantWorking, "plan"
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE "build_plans" SET "artifact_path"=.*"working_directory"=.*WHERE uuid =`).
				WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			mock.ExpectQuery(`SELECT .* FROM "build_plans"`).WithArgs("plan", 1).
				WillReturnRows(sqlmock.NewRows([]string{"uuid", "config_version", "script", "working_directory", "artifact_path"}).
					AddRow("plan", 2, script, tc.wantWorking, tc.artifact))
			plan, err := service.UpdateBuildPlan(context.Background(), "plan", BuildPlanInput{
				Name: "合法路径更新", Kind: model.BuildPlanScript, Script: script,
				WorkingDirectory: tc.working, ArtifactPath: tc.artifact, TimeoutSeconds: 30,
			})
			if err != nil || plan == nil {
				t.Fatalf("合法脚本路径未正常更新: err=%v", err)
			}
			if plan.ConfigVersion != 2 || plan.Script != script || plan.WorkingDirectory != tc.wantWorking || plan.ArtifactPath != tc.artifact {
				t.Fatal("更新后路径/脚本字节或版本不符")
			}
		})
	}
}
