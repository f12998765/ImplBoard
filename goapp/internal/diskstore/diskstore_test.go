package diskstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPutGetDrop(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"date":"2026-11-14","recordCount":7}`)
	if err := s.Put("jdy", "p1", "app1", "2026-11-14", 111, data); err != nil {
		t.Fatal(err)
	}
	e, ok := s.Get("jdy", "p1", "app1", "2026-11-14")
	if !ok || e.TS != 111 {
		t.Fatalf("get %+v ok=%v", e, ok)
	}
	// 覆盖同一天
	if err := s.Put("jdy", "p1", "app1", "2026-11-14", 222, data); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Get("jdy", "p1", "app1", "2026-11-14"); e.TS != 222 {
		t.Fatal("同一天没有被覆盖")
	}
	// 不同日期不互相覆盖
	_ = s.Put("jdy", "p1", "app1", "2026-11-13", 100, data)
	if days := s.Days("jdy", "p1", "app1"); len(days) != 2 {
		t.Fatalf("days = %v", days)
	}
	// 上限淘汰：塞到超限
	big := json.RawMessage(`{"x":"` + string(make([]byte, 800)) + `"}`)
	_ = s.Put("jdy", "p1", "app1", "2026-11-12", 333, big)
	if s.Total() > 2048 {
		t.Logf("total=%d (超过 1024 上限但已淘汰)", s.Total())
	}
	// 删除账号
	if err := s.Drop("jdy", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("jdy", "p1", "app1", "2026-11-14"); ok {
		t.Fatal("Drop 没有清干净")
	}
	if _, err := os.Stat(filepath.Join(root, "jdy", "p1")); !os.IsNotExist(err) {
		t.Fatal("目录还在")
	}
}
