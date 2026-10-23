package assistant

import (
	"context"
	"strings"
	"testing"

	"edo/internal/model"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestDeleteRequestOffersNavigationWithoutAddingDeleteTool(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "navigation-test-key")
	conversation, err := s.CreateConversation(ctx, user, "删除导航")
	if err != nil {
		t.Fatal(err)
	}
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
			if len(messages) == 0 || messages[0].Role != schema.System || !strings.Contains(messages[0].Content, deletionNavigationInstruction) {
				t.Error("模型未收到删除导航规则")
			}
			return schema.AssistantMessage("请[前往应用管理](/applications)，核对应用后在页面中确认删除。链接不会自动删除。", nil), nil
		}}, nil
	}
	for _, candidate := range s.tools(user, conversation.ID, func(Event) {}) {
		info, err := candidate.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(info.Name, "delete") || strings.Contains(info.Name, "remove") || strings.Contains(info.Name, "navigate") {
			t.Fatalf("删除导航不能注册执行或浏览器操作工具: %s", info.Name)
		}
	}
	if err := s.Chat(ctx, user, conversation.ID, "删除应用", nil); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Conversation(ctx, user, conversation.ID)
	if err != nil || len(detail.Messages) != 2 || !strings.Contains(detail.Messages[1].Content, "[前往应用管理](/applications)") {
		t.Fatalf("删除导航没有保留在历史消息: %+v %v", detail, err)
	}
	var actions, jobs int64
	if err := db.Model(&model.AssistantAction{}).Count(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Job{}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if actions != 0 || jobs != 0 {
		t.Fatalf("导航回复产生了可执行操作: actions=%d jobs=%d", actions, jobs)
	}
}
