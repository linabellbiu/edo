package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidLevel = errors.New("日志级别无效")
	ErrInvalidQuery = errors.New("系统日志查询条件无效")
)

const (
	defaultBufferCapacity  = 5000
	maximumBufferBytes     = 8 * 1024 * 1024
	maximumEntryBytes      = 16 * 1024
	maximumMessageBytes    = 8 * 1024
	maximumOperationBytes  = 256
	maximumFieldKeyBytes   = 128
	maximumFieldValueBytes = 2 * 1024
	maximumEntryFields     = 32
	cacheTruncatedMessage  = "\n…系统日志缓存已截断"
)

// Entry 是提供给系统日志页面的当前进程结构化日志快照。
type Entry struct {
	ID        uint64            `json:"id"`
	CreatedAt time.Time         `json:"created_at"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Operation string            `json:"operation,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type Query struct {
	BeforeID uint64
	Limit    int
	Level    string
	Text     string
}

type buffer struct {
	mu      sync.RWMutex
	entries []Entry
	sizes   []int
	next    int
	count   int
	nextID  uint64
	bytes   int
}

func newBuffer(capacity int) *buffer {
	if capacity < 1 {
		capacity = defaultBufferCapacity
	}
	return &buffer{entries: make([]Entry, capacity), sizes: make([]int, capacity)}
}

func (b *buffer) append(entry Entry) {
	entry, size := normalizeCachedEntry(entry)
	b.mu.Lock()
	defer b.mu.Unlock()
	// 条数和文本字节同时有界；清空被淘汰槽位，立即解除旧字符串和字段引用。
	for b.count > 0 && (b.count == len(b.entries) || b.bytes+size > maximumBufferBytes) {
		oldest := (b.next - b.count + len(b.entries)) % len(b.entries)
		b.bytes -= b.sizes[oldest]
		b.entries[oldest] = Entry{}
		b.sizes[oldest] = 0
		b.count--
	}
	b.nextID++
	entry.ID = b.nextID
	b.entries[b.next] = entry
	b.sizes[b.next] = size
	b.bytes += size
	b.next = (b.next + 1) % len(b.entries)
	b.count++
}

// 只缩减页面缓存，完整记录仍由后续 Handler 写入标准输出与文件。
// 所有字符串均复制，避免短子串让原始的大对象一直存活。
func normalizeCachedEntry(entry Entry) (Entry, int) {
	var truncated, changed bool
	entry.Level, truncated = cloneCachedText(entry.Level, 32)
	entry.Operation, changed = cloneCachedText(entry.Operation, maximumOperationBytes)
	truncated = truncated || changed
	entry.Message, changed = cloneCachedText(entry.Message, maximumMessageBytes)
	truncated = truncated || changed
	size := len(entry.Level) + len(entry.Operation) + len(entry.Message)
	remaining := maximumEntryBytes - size - len(cacheTruncatedMessage)
	var fields map[string]string
	if len(entry.Fields) > 0 {
		fields = make(map[string]string, min(len(entry.Fields), maximumEntryFields))
	}
	for key, value := range entry.Fields {
		if len(fields) == maximumEntryFields || remaining <= 0 {
			truncated = true
			break
		}
		key, changed = cloneCachedText(key, min(maximumFieldKeyBytes, remaining))
		truncated = truncated || changed
		if _, exists := fields[key]; exists {
			truncated = true
			continue
		}
		value, changed = cloneCachedText(value, min(maximumFieldValueBytes, remaining-len(key)))
		truncated = truncated || changed
		fields[key] = value
		fieldSize := len(key) + len(value)
		size += fieldSize
		remaining -= fieldSize
	}
	entry.Fields = fields
	if truncated {
		entry.Message += cacheTruncatedMessage
		size += len(cacheTruncatedMessage)
	}
	return entry, size
}

func cloneCachedText(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != ""
	}
	truncated := len(value) > limit
	if truncated {
		value = value[:limit]
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
		truncated = true
	}
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return strings.Clone(value), truncated
}

func (b *buffer) list(query Query) ([]Entry, bool, error) {
	query.Level = strings.ToLower(strings.TrimSpace(query.Level))
	query.Text = strings.ToLower(strings.TrimSpace(query.Text))
	if query.Limit < 1 || query.Limit > 500 || len(query.Text) > 200 || !validQueryLevel(query.Level) {
		return nil, false, ErrInvalidQuery
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Entry, 0, min(query.Limit+1, b.count))
	for offset := 0; offset < b.count; offset++ {
		index := (b.next - 1 - offset + len(b.entries)) % len(b.entries)
		entry := b.entries[index]
		if query.BeforeID > 0 && entry.ID >= query.BeforeID {
			continue
		}
		if query.Level != "" && entry.Level != query.Level {
			continue
		}
		if query.Text != "" && !entryMatches(entry, query.Text) {
			continue
		}
		result = append(result, cloneEntry(entry))
		if len(result) > query.Limit {
			return result[:query.Limit], true, nil
		}
	}
	return result, false, nil
}

func validQueryLevel(level string) bool {
	return level == "" || level == "debug" || level == "info" || level == "warn" || level == "error"
}

func entryMatches(entry Entry, query string) bool {
	if strings.Contains(strings.ToLower(entry.Message), query) || strings.Contains(strings.ToLower(entry.Operation), query) {
		return true
	}
	for key, value := range entry.Fields {
		if strings.Contains(strings.ToLower(key), query) || strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func cloneEntry(entry Entry) Entry {
	fields := make(map[string]string, len(entry.Fields))
	for key, value := range entry.Fields {
		fields[key] = value
	}
	entry.Fields = fields
	return entry
}

// RuntimeController 保存可在进程运行期间安全修改的日志过滤设置。
// slog.LevelVar 由标准库 Handler 直接读取，不需要重建全局 Logger。
type RuntimeController struct {
	level           slog.LevelVar
	httpAccess      atomic.Bool
	logs            *buffer
	output          *runtimeOutput
	maintenanceWake chan struct{}
}

func New(level string) *slog.Logger {
	logger, _ := NewRuntime(level)
	return logger
}

func NewRuntime(level string) (*slog.Logger, *RuntimeController) {
	return newRuntime(level, os.Stdout, defaultBufferCapacity)
}

func NewRuntimeWithFile(level string, fileSettings FileSettings) (*slog.Logger, *RuntimeController, error) {
	return newRuntimeWithFile(level, os.Stdout, defaultBufferCapacity, fileSettings)
}

func newRuntime(level string, output io.Writer, capacity int) (*slog.Logger, *RuntimeController) {
	logger, controller, _ := newRuntimeWithFile(level, output, capacity, FileSettings{})
	return logger, controller
}

func newRuntimeWithFile(level string, output io.Writer, capacity int, fileSettings FileSettings) (*slog.Logger, *RuntimeController, error) {
	runtimeOutput := newRuntimeOutput(output)
	controller := &RuntimeController{
		logs: newBuffer(capacity), output: runtimeOutput, maintenanceWake: make(chan struct{}, 1),
	}
	if err := controller.Apply(level, true); err != nil {
		_ = controller.Apply("info", true)
	}
	if fileSettings != (FileSettings{}) {
		if err := runtimeOutput.configure(fileSettings); err != nil {
			return nil, nil, err
		}
	}
	outputHandler := slog.NewJSONHandler(runtimeOutput, &slog.HandlerOptions{Level: &controller.level})
	logger := slog.New(&captureHandler{next: outputHandler, logs: controller.logs})
	return logger, controller, nil
}

func (c *RuntimeController) Apply(level string, httpAccess bool) error {
	parsed, err := parseLevel(level)
	if err != nil {
		return err
	}
	c.level.Set(parsed)
	c.httpAccess.Store(httpAccess)
	return nil
}

func (c *RuntimeController) ApplyFileSettings(settings FileSettings) error {
	if c == nil || c.output == nil {
		return errors.New("运行日志输出未初始化")
	}
	if err := c.output.configure(settings); err != nil {
		return err
	}
	select {
	case c.maintenanceWake <- struct{}{}:
	default:
	}
	return nil
}

func (c *RuntimeController) ApplySettings(level string, httpAccess bool, fileSettings FileSettings) error {
	parsed, err := parseLevel(level)
	if err != nil {
		return err
	}
	if err := c.ApplyFileSettings(fileSettings); err != nil {
		return err
	}
	c.level.Set(parsed)
	c.httpAccess.Store(httpAccess)
	return nil
}

func (c *RuntimeController) FileSettings() FileSettings {
	if c == nil || c.output == nil {
		return DefaultFileSettings()
	}
	return c.output.fileSettings()
}

func (c *RuntimeController) Close() error {
	if c == nil || c.output == nil {
		return nil
	}
	return c.output.close()
}

func (c *RuntimeController) Level() string {
	switch c.level.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

func (c *RuntimeController) HTTPAccessEnabled() bool {
	return c == nil || c.httpAccess.Load()
}

func (c *RuntimeController) List(query Query) ([]Entry, bool, error) {
	if c == nil || c.logs == nil {
		return nil, false, errors.New("系统日志缓冲未初始化")
	}
	return c.logs.list(query)
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, ErrInvalidLevel
	}
}

type captureHandler struct {
	next   slog.Handler
	logs   *buffer
	fields map[string]string
	groups []string
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := make(map[string]string, len(h.fields)+record.NumAttrs())
	for key, value := range h.fields {
		fields[key] = value
	}
	record.Attrs(func(attr slog.Attr) bool {
		appendAttr(fields, h.groups, attr)
		return true
	})
	operation := fields["operation"]
	delete(fields, "operation")
	h.logs.append(Entry{
		CreatedAt: record.Time,
		Level:     normalizedLevel(record.Level),
		Message:   record.Message,
		Operation: operation,
		Fields:    fields,
	})
	return h.next.Handle(ctx, record)
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	fields := make(map[string]string, len(h.fields)+len(attrs))
	for key, value := range h.fields {
		fields[key] = value
	}
	for _, attr := range attrs {
		appendAttr(fields, h.groups, attr)
	}
	return &captureHandler{next: h.next.WithAttrs(attrs), logs: h.logs, fields: fields, groups: slices.Clone(h.groups)}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	groups := slices.Clone(h.groups)
	if name != "" {
		groups = append(groups, name)
	}
	return &captureHandler{next: h.next.WithGroup(name), logs: h.logs, fields: cloneFields(h.fields), groups: groups}
}

func appendAttr(fields map[string]string, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		nestedGroups := groups
		if attr.Key != "" {
			nestedGroups = append(slices.Clone(groups), attr.Key)
		}
		for _, nested := range attr.Value.Group() {
			appendAttr(fields, nestedGroups, nested)
		}
		return
	}
	keyParts := append(slices.Clone(groups), attr.Key)
	fields[strings.Join(keyParts, ".")] = logValue(attr.Value)
}

func logValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindBool:
		return fmt.Sprintf("%t", value.Bool())
	case slog.KindInt64:
		if value.Any() != nil {
			return fmt.Sprint(value.Any())
		}
		return fmt.Sprintf("%d", value.Int64())
	case slog.KindUint64:
		return fmt.Sprintf("%d", value.Uint64())
	case slog.KindFloat64:
		return fmt.Sprintf("%g", value.Float64())
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(value.Any())
	}
}

func normalizedLevel(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	case level >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

func cloneFields(fields map[string]string) map[string]string {
	cloned := make(map[string]string, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}
