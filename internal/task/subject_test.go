package task

import "testing"

func TestTransportSubjectRequiresExactTaskIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, logical, kind, prefix, want string
	}{
		{"默认兼容", "edo.task.pipeline.build", "pipeline.build", LogicalSubjectPrefix, "edo.task.pipeline.build"},
		{"缺少配置不能静默回退", "edo.task.pipeline.build", "pipeline.build", "", ""},
		{"自定义命名空间", "edo.task.pipeline.build", "pipeline.build", "edo.permissions.20261023.task", "edo.permissions.20261023.task.pipeline.build"},
		{"配置允许的中文前缀", "edo.task.pipeline.build", "pipeline.build", "edo.任务.发布", "edo.任务.发布.pipeline.build"},
		{"配置允许的标点前缀", "edo.task.pipeline.build", "pipeline.build", "edo.team:v1.task", "edo.team:v1.task.pipeline.build"},
		{"配置原本拒绝的前缀", "edo.task.pipeline.build", "pipeline.build", "custom.task", ""},
		{"错误类型", "edo.task.pipeline.deploy", "pipeline.build", "edo.custom.task", ""},
		{"死信不能转为任务", "edo.dead.task.v1", "pipeline.build", "edo.custom.task", ""},
		{"外部事件不能转为任务", "edo.external.pipeline.build", "pipeline.build", "edo.custom.task", ""},
		{"已经是传输主题不能再映射", "edo.custom.task.pipeline.build", "pipeline.build", "edo.custom.task", ""},
		{"类型中通配符", "edo.task.pipeline.>", "pipeline.>", "edo.custom.task", ""},
		{"类型中空分段", "edo.task.pipeline..build", "pipeline..build", "edo.custom.task", ""},
		{"前缀中通配符", "edo.task.pipeline.build", "pipeline.build", "edo.custom.*", ""},
		{"前缀中空分段", "edo.task.pipeline.build", "pipeline.build", "edo..custom.task", ""},
		{"前缀中空格", "edo.task.pipeline.build", "pipeline.build", " edo.custom.task", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TransportSubject(tt.logical, tt.kind, tt.prefix)
			if tt.want == "" {
				if err == nil || got != "" {
					t.Fatalf("不合法主题被映射：got=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("主题映射不正确：got=%q want=%q err=%v", got, tt.want, err)
			}
		})
	}
}
