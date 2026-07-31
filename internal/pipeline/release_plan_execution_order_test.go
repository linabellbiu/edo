package pipeline

import (
	"reflect"
	"testing"

	"edo/internal/model"
)

func TestReleasePlanExecutionParallelStartsAllApplicationsInSavedOrder(t *testing.T) {
	items := map[string]*model.ReleasePlanExecutionItem{
		"dragged-first":  {ID: "dragged-first", Status: model.ReleasePlanExecutionItemPending},
		"dragged-second": {ID: "dragged-second", Status: model.ReleasePlanExecutionItemPending},
	}
	snapshot := releasePlanExecutionSnapshot{Groups: []releasePlanExecutionGroupSnapshot{{
		ID: "group", Mode: model.ReleaseGroupParallel,
		ItemIDs: []string{"dragged-first", "dragged-second"},
	}}}

	started := releasePlanExecutionStartItems(snapshot, items, nil)
	if !reflect.DeepEqual(started, []string{"dragged-first", "dragged-second"}) {
		t.Fatalf("并行发布组应一次启动全部应用: %v", started)
	}
}

func TestReleasePlanExecutionSequentialFollowsDraggedApplicationOrder(t *testing.T) {
	items := map[string]*model.ReleasePlanExecutionItem{
		"dragged-first":  {ID: "dragged-first", Status: model.ReleasePlanExecutionItemPending},
		"dragged-second": {ID: "dragged-second", Status: model.ReleasePlanExecutionItemPending},
	}
	snapshot := releasePlanExecutionSnapshot{Groups: []releasePlanExecutionGroupSnapshot{{
		ID: "group", Mode: model.ReleaseGroupSequential,
		ItemIDs: []string{"dragged-first", "dragged-second"},
	}}}

	if started := releasePlanExecutionStartItems(snapshot, items, nil); !reflect.DeepEqual(started, []string{"dragged-first"}) {
		t.Fatalf("串行发布组没有先启动拖拽顺序中的第一个应用: %v", started)
	}
	items["dragged-first"].Status = model.ReleasePlanExecutionItemRunning
	if started := releasePlanExecutionStartItems(snapshot, items, nil); len(started) != 0 {
		t.Fatalf("前一个应用执行中时不应启动后续应用: %v", started)
	}
	items["dragged-first"].Status = model.ReleasePlanExecutionItemSucceeded
	if started := releasePlanExecutionStartItems(snapshot, items, nil); !reflect.DeepEqual(started, []string{"dragged-second"}) {
		t.Fatalf("前一个应用完成后没有按拖拽顺序启动下一个应用: %v", started)
	}
}
