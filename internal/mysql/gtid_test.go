package mysql

import (
	"testing"
)

func TestParseGTIDSet(t *testing.T) {
	raw := "2635955b-800d-11e9-9231-0242ac110002:1-100:105-120,\n49080271-800e-11e9-9231-0242ac110002:1-50"
	gtidSet, err := ParseGTIDSet(raw)
	if err != nil {
		t.Fatalf("ParseGTIDSet failed: %v", err)
	}

	if len(gtidSet.Sets) != 2 {
		t.Fatalf("expected 2 UUID sets, got %d", len(gtidSet.Sets))
	}

	u1 := gtidSet.Sets["2635955b-800d-11e9-9231-0242ac110002"]
	if u1 == nil {
		t.Fatalf("expected UUID 1 to exist")
	}
	// 1-100 (100) + 105-120 (16) = 116
	if u1.TotalTransactions() != 116 {
		t.Errorf("expected 116 transactions for u1, got %d", u1.TotalTransactions())
	}

	total := gtidSet.TotalTransactions()
	if total != 166 {
		t.Errorf("expected 166 total transactions, got %d", total)
	}
}

func TestContainsAndCompare(t *testing.T) {
	masterRaw := "uuid-1:1-100,uuid-2:1-50"
	slaveAdvancedRaw := "uuid-1:1-95,uuid-2:1-50"
	slaveLaggingRaw := "uuid-1:1-80,uuid-2:1-30"

	master, err := ParseGTIDSet(masterRaw)
	if err != nil {
		t.Fatal(err)
	}

	slaveAdv, err := ParseGTIDSet(slaveAdvancedRaw)
	if err != nil {
		t.Fatal(err)
	}

	slaveLag, err := ParseGTIDSet(slaveLaggingRaw)
	if err != nil {
		t.Fatal(err)
	}

	if !master.Contains(slaveAdv) {
		t.Errorf("master should contain slaveAdv")
	}
	if slaveAdv.Contains(master) {
		t.Errorf("slaveAdv should not contain master")
	}

	if !slaveAdv.Contains(slaveLag) {
		t.Errorf("slaveAdv should contain slaveLag")
	}

	// Compare slaveAdv vs slaveLag
	cmp := CompareGTIDSets(slaveAdv, slaveLag)
	if cmp != 1 {
		t.Errorf("expected slaveAdv > slaveLag (cmp=1), got %d", cmp)
	}

	cmpReverse := CompareGTIDSets(slaveLag, slaveAdv)
	if cmpReverse != -1 {
		t.Errorf("expected slaveLag < slaveAdv (cmp=-1), got %d", cmpReverse)
	}

	// Compare identical
	if CompareGTIDSets(slaveAdv, slaveAdv) != 0 {
		t.Errorf("identical comparison should be 0")
	}
}
