package store

import (
	"testing"
	"time"
)

func TestClipsSearchAndExpiry(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	past := now.Add(-time.Second)
	add := func(id, content string, at time.Time, expires *time.Time) {
		t.Helper()
		if ok, err := s.AddClip(Clip{ID: id, Content: content, DeviceID: "mac", DeviceName: "Mac", CreatedAt: at, ExpiresAt: expires}); err != nil || !ok {
			t.Fatalf("AddClip(%s) = %v, %v", id, ok, err)
		}
	}
	add("1", "docker compose up -d", now.Add(-3*time.Minute), nil)
	add("2", "Kahvaltı menüsü: simit, çay", now.Add(-2*time.Minute), nil)
	add("3", "expired secret", now.Add(-time.Minute), &past)

	if ok, _ := s.AddClip(Clip{ID: "1", Content: "dup", CreatedAt: now}); ok {
		t.Fatal("duplicate id was inserted")
	}

	all, _ := s.ListClips(Query{})
	if len(all) != 2 || all[0].ID != "2" {
		t.Fatalf("list = %+v, want [2 1] (newest first, expired hidden)", all)
	}
	hit, _ := s.ListClips(Query{Text: "compose"})
	if len(hit) != 1 || hit[0].ID != "1" {
		t.Fatalf("search compose = %+v", hit)
	}
	hit, _ = s.ListClips(Query{Text: "menüsü"})
	if len(hit) != 1 || hit[0].ID != "2" {
		t.Fatalf("search unicode = %+v", hit)
	}
	hit, _ = s.ListClips(Query{Text: "up"})
	if len(hit) != 1 || hit[0].ID != "1" {
		t.Fatalf("short search = %+v", hit)
	}

	gone, _ := s.DeleteExpired(now)
	if len(gone) != 1 || gone[0].ID != "3" {
		t.Fatalf("DeleteExpired = %+v", gone)
	}

	s.SetPinned("1", true)
	removed, _ := s.Trim(0)
	if len(removed) != 1 || removed[0] != "2" {
		t.Fatalf("Trim removed %v, want [2] (pinned kept)", removed)
	}
	if hit, _ = s.ListClips(Query{Text: "menüsü"}); len(hit) != 0 {
		t.Fatalf("fts still finds deleted clip: %+v", hit)
	}
}
