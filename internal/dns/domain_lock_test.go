package dnsmanager

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"edo/internal/testutil"
)

func TestInvalidDomainRequestsDoNotRetainLocks(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	service := NewService(db, nil, nil)
	operations := []func(string) error{
		func(id string) error { _, err := service.ListRecords(context.Background(), id); return err },
		func(id string) error {
			_, err := service.CreateRecord(context.Background(), id, RecordInput{})
			return err
		},
		func(id string) error {
			_, err := service.UpdateRecord(context.Background(), id, "record", RecordInput{})
			return err
		},
		func(id string) error { return service.DeleteRecord(context.Background(), id, "record") },
	}
	for index := range 250 {
		id := fmt.Sprintf("missing-domain-%d", index)
		for _, operation := range operations {
			mock.ExpectQuery(`SELECT .* FROM "dns_domains"`).WithArgs(id, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			if err := operation(id); !errors.Is(err, ErrDomainNotFound) {
				t.Fatalf("不存在域名的错误发生变化: %v", err)
			}
			assertNoDomainLocks(t, service)
		}
	}
}

func TestDomainLockKeepsWaitersOnSameLock(t *testing.T) {
	service := &Service{}
	releaseFirst := service.domainLock("same-domain")
	defer releaseFirst()
	service.locksMu.Lock()
	first := service.locks["same-domain"]
	service.locksMu.Unlock()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var releaseWaiter sync.Once
	t.Cleanup(func() { releaseWaiter.Do(func() { close(proceed) }) })
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		release := service.domainLock("same-domain")
		defer release()
		close(entered)
		<-proceed
	}()
	waitDomainLockReferences(t, service, "same-domain", 2)
	releaseFirst()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("等待中的同域名操作没有得到锁")
	}
	service.locksMu.Lock()
	current := service.locks["same-domain"]
	service.locksMu.Unlock()
	if current != first {
		t.Fatal("仍有等待者持锁时原互斥锁被移除或替换")
	}
	newcomerEntered := make(chan struct{})
	go func() {
		release := service.domainLock("same-domain")
		defer release()
		close(newcomerEntered)
	}()
	waitDomainLockReferences(t, service, "same-domain", 2)
	select {
	case <-newcomerEntered:
		t.Fatal("新请求绕过了已排队请求持有的同域名互斥锁")
	default:
	}
	releaseWaiter.Do(func() { close(proceed) })
	<-waiterDone
	select {
	case <-newcomerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("前一个持有者退出后新请求仍然阻塞")
	}
	// newcomerEntered 在释放前关闭，等待其完成引用回收。
	waitDomainLockReferences(t, service, "same-domain", 0)
	assertNoDomainLocks(t, service)
}

func TestDomainLockSerializesConcurrentOperationsAndReclaimsKeys(t *testing.T) {
	service := &Service{}
	var pending sync.WaitGroup
	var active, overlap atomic.Int32
	for range 16 {
		pending.Add(1)
		go func() {
			defer pending.Done()
			for range 100 {
				release := service.domainLock("same-domain")
				if active.Add(1) != 1 {
					overlap.Add(1)
				}
				runtime.Gosched()
				active.Add(-1)
				release()
			}
		}()
	}
	pending.Wait()
	if overlap.Load() != 0 {
		t.Fatalf("同一域名出现并发临界区: overlaps=%d", overlap.Load())
	}
	assertNoDomainLocks(t, service)
}

func TestDomainLockDoesNotBlockIndependentKeys(t *testing.T) {
	service := &Service{}
	releaseFirst := service.domainLock("first-domain")
	defer releaseFirst()
	done := make(chan struct{})
	go func() {
		release := service.domainLock("second-domain")
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("不同域名的操作被同一把锁阻塞")
	}
	releaseFirst()
	assertNoDomainLocks(t, service)
}

func TestDomainOperationsReleaseLocksAfterErrors(t *testing.T) {
	service, _, provider := newDNSTestService(t)
	account, err := service.CreateProviderAccount(context.Background(), "admin", ProviderAccountInput{
		Name: "测试 DNS", Provider: "test", Config: map[string]string{"token": "test-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	domain, err := service.CreateDomain(context.Background(), "admin", DomainInput{AccountID: account.ID, Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	input := RecordInput{Name: "www", Type: "A", Value: "192.0.2.1", TTL: 300}
	provider.err = errors.New("模拟 DNS 厂商失败")
	operations := []func() error{
		func() error { _, err := service.ListRecords(context.Background(), domain.ID); return err },
		func() error { _, err := service.CreateRecord(context.Background(), domain.ID, input); return err },
		func() error {
			_, err := service.UpdateRecord(context.Background(), domain.ID, "record", input)
			return err
		},
		func() error { return service.DeleteRecord(context.Background(), domain.ID, "record") },
	}
	for _, operation := range operations {
		if err := operation(); !errors.Is(err, ErrProviderRequest) {
			t.Fatalf("厂商异常的错误返回发生变化: %v", err)
		}
		assertNoDomainLocks(t, service)
	}
	provider.err = nil
	if _, err := service.CreateRecord(context.Background(), domain.ID, RecordInput{}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("无效解析记录的错误返回发生变化: %v", err)
	}
	assertNoDomainLocks(t, service)
	if err := service.SetDomainActive(context.Background(), domain.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListRecords(context.Background(), domain.ID); !errors.Is(err, ErrDomainDisabled) {
		t.Fatalf("停用域名的错误返回发生变化: %v", err)
	}
	assertNoDomainLocks(t, service)
}

func waitDomainLockReferences(t *testing.T, service *Service, key string, expected int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		service.locksMu.Lock()
		references := 0
		if lock := service.locks[key]; lock != nil {
			references = lock.references
		}
		service.locksMu.Unlock()
		if references == expected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("域名锁引用未达到预期: got=%d want=%d", references, expected)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertNoDomainLocks(t *testing.T, service *Service) {
	t.Helper()
	service.locksMu.Lock()
	defer service.locksMu.Unlock()
	if len(service.locks) != 0 {
		t.Fatalf("操作结束后域名锁未回收: count=%d", len(service.locks))
	}
}
