package shardmap

import "testing"

func TestRangesAndValidation(t *testing.T) {
	m := Map{[]Shard{{"a", 1, 1000, []string{"http://a1:3000", "http://a2:3000"}}, {"b", 1001, 2000, []string{"http://b1:3000", "http://b2:3000"}}}}
	if e := m.Validate(); e != nil {
		t.Fatal(e)
	}
	for id, want := range map[string]string{"A0001": "a", "A1000": "a", "A1001": "b", "A2000": "b"} {
		s, e := m.Owner(id)
		if e != nil || s.ID != want {
			t.Fatalf("%s: %+v %v", id, s, e)
		}
	}
	for _, id := range []string{"A0000", "A2001", "A1", "a0001", "A+001", "A 001", "A00001"} {
		if _, e := m.Owner(id); e == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	m.Shards[1].First = 1000
	if m.Validate() == nil {
		t.Fatal("overlap accepted")
	}
	m.Shards[1].First = 1002
	if m.Validate() == nil {
		t.Fatal("gap accepted")
	}
}
