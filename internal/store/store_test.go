package store

import (
	"path/filepath"
	"testing"
)

func TestStoreRoundtrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.EnsureAdmin(684693302); err != nil {
		t.Fatal(err)
	}
	u, ok, err := s.GetUser(684693302)
	if err != nil || !ok || !u.IsAdmin || u.Status != StatusApproved {
		t.Fatalf("admin: %+v ok=%v err=%v", u, ok, err)
	}

	if _, err := s.UpsertUser(111, "bob"); err != nil {
		t.Fatal(err)
	}
	u, _, _ = s.GetUser(111)
	if u.Status != StatusPending {
		t.Fatalf("new user should be pending, got %q", u.Status)
	}
	if err := s.SetUserStatus(111, StatusApproved); err != nil {
		t.Fatal(err)
	}

	st, err := s.AddStream(Stream{Owner: 111, Name: "Двор", URL: "rtsp://x/1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.ID == 0 || len(st.Classes) != 1 || st.Classes[0] != "person" || st.Conf != 0.35 {
		t.Fatalf("defaults not applied: %+v", st)
	}

	st.Classes = []string{"person", "car"}
	st.Rotate = "90cw"
	if err := s.UpdateStream(st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetStream(st.ID)
	if err != nil || !ok || len(got.Classes) != 2 || got.Rotate != "90cw" {
		t.Fatalf("update roundtrip: %+v ok=%v err=%v", got, ok, err)
	}

	list, err := s.ListStreamsByOwner(111)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	n, _ := s.CountStreamsByOwner(111)
	if n != 1 {
		t.Fatalf("count %d", n)
	}
	if err := s.DeleteStream(st.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountStreamsByOwner(111); n != 0 {
		t.Fatalf("count after delete %d", n)
	}
}
