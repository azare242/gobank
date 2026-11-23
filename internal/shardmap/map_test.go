package shardmap

import "testing"

func TestRangesAndValidation(t *testing.T) {
	m := Map{Shards: []Shard{{ID: "a", First: 1, Last: 1000, Replicas: []string{"http://a1:3000", "http://a2:3000"}}, {ID: "b", First: 1001, Last: 2000, Replicas: []string{"http://b1:3000", "http://b2:3000"}}}}
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

func TestDiscoveryValidation(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		count int
		valid bool
	}{{"", 1, false}, {"", 2, true}, {"dns", 1, true}, {"dns", 0, false}, {"other", 2, false}} {
		m := Map{Shards: []Shard{{ID: "a", First: 1, Last: 1000, Discovery: tc.mode, Replicas: []string{"http://a1", "http://a2"}[:tc.count]}, {ID: "b", First: 1001, Last: 2000, Replicas: []string{"http://b1", "http://b2"}}}}
		if err := m.Validate(); (err == nil) != tc.valid {
			t.Fatalf("mode=%q count=%d: %v", tc.mode, tc.count, err)
		}
	}
	m := Map{Shards: []Shard{{ID: "a", First: 1, Last: 1000, Discovery: "dns", Replicas: []string{"http://a", "http://a"}}, {ID: "b", First: 1001, Last: 2000, Replicas: []string{"http://b1", "http://b2"}}}}
	if m.Validate() == nil {
		t.Fatal("duplicate DNS URL accepted")
	}
}
