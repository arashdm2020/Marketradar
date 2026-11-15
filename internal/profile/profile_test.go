package profile

import (
	"path/filepath"
	"testing"
)

func TestPersistenceIsolation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.db")
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.Ensure(101)
	if e != nil {
		t.Fatal(e)
	}
	a.Markets = "XAU/USD"
	a.MinProfit = 0.5
	a.MaxProfit = 2
	a.MaxRisk = 1
	a.Completed = true
	a.State = "menu"
	if e = s.Save(a); e != nil {
		t.Fatal(e)
	}
	s.DB.Close()
	s, e = Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e = s.Get(101)
	if e != nil || !a.Completed || a.Markets != "XAU/USD" {
		t.Fatal(a, e)
	}
	b, e := s.Ensure(202)
	if e != nil || b.Completed || b.Markets != "" {
		t.Fatal(b, e)
	}
	if e = s.Record(101, "XAU/USD", "4h", "swing", "{}"); e != nil {
		t.Fatal(e)
	}
	h, _ := s.History(202)
	if len(h) != 0 {
		t.Fatal("history leaked")
	}
	if e = s.Delete(101); e != nil {
		t.Fatal(e)
	}
	h, _ = s.History(101)
	if len(h) != 0 {
		t.Fatal("history retained")
	}
}
