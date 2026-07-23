package main

import "testing"

func TestSameDatabaseRecognizesEquivalentSQLiteDSNs(t *testing.T) {
	if !sameDatabase("sqlite", "data/legacy.db", "sqlite", "file:data/legacy.db?_busy_timeout=5000") {
		t.Fatal("等价 SQLite 路径未被识别")
	}
	if sameDatabase("sqlite", "data/source.db", "sqlite", "data/destination.db") {
		t.Fatal("不同 SQLite 路径被错误识别为同一数据库")
	}
}

func TestReadOnlySQLiteDSN(t *testing.T) {
	dsn, err := readOnlySQLiteDSN("data/legacy.db")
	if err != nil || dsn != "file:data/legacy.db?mode=ro" {
		t.Fatalf("生成只读 SQLite DSN 失败: dsn=%q err=%v", dsn, err)
	}
	if _, err := readOnlySQLiteDSN(":memory:"); err == nil {
		t.Fatal("内存 SQLite 不应作为旧数据来源")
	}
	if _, err := readOnlySQLiteDSN("file:data/legacy.db?mode=rw"); err == nil {
		t.Fatal("可写 SQLite DSN 应被拒绝")
	}
}
