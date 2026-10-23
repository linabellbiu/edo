package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestRuntimeLogBufferEvictsByBytesAndReleasesSlots(t *testing.T) {
	logs := newBuffer(defaultBufferCapacity)
	for range 1000 {
		logs.append(Entry{
			Level: "info", Message: strings.Repeat("m", maximumMessageBytes),
			Fields: map[string]string{"first": strings.Repeat("v", maximumFieldValueBytes), "second": strings.Repeat("w", maximumFieldValueBytes)},
		})
	}
	if logs.bytes > maximumBufferBytes || logs.count >= 1000 {
		t.Fatalf("日志字节预算没有淘汰旧记录: bytes=%d count=%d", logs.bytes, logs.count)
	}
	var total, retained int
	for index, entry := range logs.entries {
		if entry.ID == 0 {
			if entry.Message != "" || entry.Fields != nil || logs.sizes[index] != 0 {
				t.Fatal("被淘汰的日志槽位仍持有记录")
			}
			continue
		}
		retained++
		size := cachedEntryTextBytes(entry)
		if size != logs.sizes[index] || size > maximumEntryBytes {
			t.Fatalf("缓存条目字节统计错误: actual=%d recorded=%d", size, logs.sizes[index])
		}
		total += size
	}
	if total != logs.bytes || retained != logs.count {
		t.Fatalf("日志累计字节或条数不匹配: total=%d bytes=%d retained=%d count=%d", total, logs.bytes, retained, logs.count)
	}
	entries, _, err := logs.list(Query{Limit: 500})
	if err != nil || len(entries) == 0 || entries[0].ID != 1000 {
		t.Fatalf("字节淘汰后没有保留最新记录: len=%d err=%v", len(entries), err)
	}
}

func TestRuntimeLogBufferKeepsCountLimit(t *testing.T) {
	logs := newBuffer(3)
	for range 10 {
		logs.append(Entry{Message: "small"})
	}
	if logs.count != 3 || logs.bytes != 3*len("small") {
		t.Fatalf("覆盖后预算错误: count=%d bytes=%d", logs.count, logs.bytes)
	}
	entries, _, err := logs.list(Query{Limit: 10})
	if err != nil || len(entries) != 3 || entries[0].ID != 10 || entries[2].ID != 8 {
		t.Fatalf("覆盖后的分页顺序错误: entries=%+v err=%v", entries, err)
	}
}

func TestRuntimeLogCacheBoundsFieldsAndRepairsUTF8(t *testing.T) {
	fields := make(map[string]string)
	for index := range 100 {
		fields[fmt.Sprintf("字段%d-%s", index, strings.Repeat("键", 100))] = strings.Repeat("值", maximumFieldValueBytes)
	}
	entry, size := normalizeCachedEntry(Entry{
		Level: "info", Message: strings.Repeat("消息", maximumMessageBytes) + "\xff",
		Operation: strings.Repeat("操作", maximumOperationBytes), Fields: fields,
	})
	if size > maximumEntryBytes || size != cachedEntryTextBytes(entry) || len(entry.Fields) > maximumEntryFields ||
		len(entry.Operation) > maximumOperationBytes || !strings.HasSuffix(entry.Message, cacheTruncatedMessage) {
		t.Fatalf("单条日志没有遵循预算: bytes=%d fields=%d operation=%d", size, len(entry.Fields), len(entry.Operation))
	}
	if !utf8.ValidString(entry.Message) || !utf8.ValidString(entry.Operation) {
		t.Fatal("截断后的日志不是有效 UTF-8")
	}
	for key, value := range entry.Fields {
		if len(key) > maximumFieldKeyBytes || len(value) > maximumFieldValueBytes || !utf8.ValidString(key) || !utf8.ValidString(value) {
			t.Fatal("日志字段没有保持长度与 UTF-8 边界")
		}
	}
	text, changed := cloneCachedText("前\xff后", 32)
	if !changed || text != "前�后" {
		t.Fatalf("无效 UTF-8 修复错误: text=%q changed=%v", text, changed)
	}
	smallFields := make(map[string]string)
	for index := range 100 {
		smallFields[fmt.Sprintf("field-%d", index)] = "small"
	}
	entry, _ = normalizeCachedEntry(Entry{Message: "字段较多", Fields: smallFields})
	if len(entry.Fields) != maximumEntryFields || !strings.HasSuffix(entry.Message, cacheTruncatedMessage) {
		t.Fatalf("短字段没有遵循数量上限: fields=%d", len(entry.Fields))
	}
}

func TestRuntimeLogCacheCopiesStringsAndFieldSnapshots(t *testing.T) {
	large := strings.Repeat("0123456789", 1024*1024)
	fields := map[string]string{large[20:25]: large[30:36]}
	logs := newBuffer(1)
	logs.append(Entry{Message: large[:8], Operation: large[10:16], Fields: fields})
	fields[large[20:25]] = "mutated"
	entries, _, err := logs.list(Query{Limit: 1})
	if err != nil || len(entries) != 1 || entries[0].Fields[large[20:25]] != large[30:36] {
		t.Fatalf("字段没有保存独立快照: entries=%+v err=%v", entries, err)
	}
	base := uintptr(unsafe.Pointer(unsafe.StringData(large)))
	assertDetached := func(value string) {
		t.Helper()
		if value == "" {
			return
		}
		address := uintptr(unsafe.Pointer(unsafe.StringData(value)))
		if address >= base && address < base+uintptr(len(large)) {
			t.Fatal("缓存短字符串仍引用原始大字符串的底层内存")
		}
	}
	assertDetached(entries[0].Message)
	assertDetached(entries[0].Operation)
	for key, value := range entries[0].Fields {
		assertDetached(key)
		assertDetached(value)
	}
}

type redactedLogValue struct{}

func (redactedLogValue) LogValue() slog.Value { return slog.StringValue("[已隐藏]") }

func TestRuntimeLogCacheLeavesFullFileAndConsoleOutput(t *testing.T) {
	var console bytes.Buffer
	directory := t.TempDir()
	logger, controller, err := newRuntimeWithFile("info", &console, 10, FileSettings{
		Enabled: true, Directory: directory, MaxFileSizeMB: 1, CompressAfterDays: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	message := strings.Repeat("m", maximumMessageBytes*2) + "message-tail"
	field := strings.Repeat("v", maximumFieldValueBytes*2) + "field-tail"
	logger.Info(message, "payload", field, "credential", redactedLogValue{})
	entries, _, err := controller.List(Query{Limit: 10})
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Message, cacheTruncatedMessage) ||
		entries[0].Fields["credential"] != "[已隐藏]" {
		t.Fatalf("页面缓存截断或脱敏值错误: entries=%+v err=%v", entries, err)
	}
	file, err := os.ReadFile(filepath.Join(directory, activeLogFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range [][]byte{console.Bytes(), file} {
		if !bytes.Contains(output, []byte(message)) || !bytes.Contains(output, []byte(field)) || !bytes.Contains(output, []byte("[已隐藏]")) {
			t.Fatal("页面缓存限制改变了完整日志输出或预先脱敏字段")
		}
	}
}

func TestRuntimeLogBufferConcurrentAppendAndList(t *testing.T) {
	logger, controller := newRuntime("info", io.Discard, 200)
	var pending sync.WaitGroup
	for worker := range 8 {
		pending.Add(1)
		go func() {
			defer pending.Done()
			for index := range 100 {
				logger.LogAttrs(context.Background(), slog.LevelInfo, "并发日志", slog.Int("worker", worker), slog.Int("index", index))
				entries, _, err := controller.List(Query{Limit: 10})
				if err != nil {
					t.Errorf("并发查询日志失败: %v", err)
					return
				}
				if len(entries) > 0 {
					entries[0].Fields["caller"] = "changed"
				}
			}
		}()
	}
	pending.Wait()
	if controller.logs.count != 200 || controller.logs.bytes > maximumBufferBytes {
		t.Fatalf("并发写入后的预算错误: count=%d bytes=%d", controller.logs.count, controller.logs.bytes)
	}
}

func cachedEntryTextBytes(entry Entry) int {
	total := len(entry.Level) + len(entry.Message) + len(entry.Operation)
	for key, value := range entry.Fields {
		total += len(key) + len(value)
	}
	return total
}

func BenchmarkRuntimeLogBufferAppend(b *testing.B) {
	entry := Entry{Level: "info", Message: "流水线状态已更新", Operation: "pipeline_update", Fields: map[string]string{"pipeline_run_id": "run-1", "status": "running"}}
	logs := newBuffer(defaultBufferCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		logs.append(entry)
	}
}
