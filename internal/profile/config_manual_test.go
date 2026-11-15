package profile

import "testing"

func TestManualCandleConfigDraftAndRollback(t *testing.T) {
	s, e := Open(t.TempDir() + "/config.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	for _, v := range [][3]int{{249, 50, 12}, {500, 501, 12}, {1001, 50, 12}, {500, 49, 12}, {500, 50, 0}, {500, 50, 97}} {
		if _, e := s.DraftConfig(AdminID, v[0], v[1], v[2], 180, 4096); e == nil {
			t.Fatalf("accepted invalid %v", v)
		}
	}
	draft, e := s.DraftConfig(AdminID, 500, 50, 24, 180, 4096)
	if e != nil {
		t.Fatal(e)
	}
	active, e := s.ActiveConfig()
	if e != nil || active.Version != 1 || active.EngineCandles != 500 || active.ModelCandles != 500 {
		t.Fatal("draft changed active config")
	}
	if e = s.SetConfig(AdminID, draft.Version); e != nil {
		t.Fatal(e)
	}
	active, e = s.ActiveConfig()
	if e != nil || active.HorizonCandles != 24 || active.ModelCandles != 50 || active.MaxDurationSeconds != 180 || active.ResponseTokenBudget != 4096 {
		t.Fatal("publish failed")
	}
	if e = s.SetConfig(AdminID, 1); e != nil {
		t.Fatal(e)
	}
	active, e = s.ActiveConfig()
	if e != nil || active.ModelCandles != 500 || active.HorizonCandles != 12 {
		t.Fatal("rollback failed")
	}
}
