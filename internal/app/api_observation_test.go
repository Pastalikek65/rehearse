package app

import (
	"testing"
)

func apiObservationFixture() map[string][]byte {
	return map[string][]byte{
		"/v1/me":      []byte(`{"id":7,"username":"synthetic-reader","is_admin":true}`),
		"/v1/version": []byte(`{"version":"2.2.19","commit":"abc123","arch":"amd64","os":"linux"}`),
		"/v1/categories?counts=true": []byte(`[
			{"id":11,"user_id":7,"title":"General","hide_globally":false,"feed_count":1,"total_unread":1},
			{"id":12,"user_id":7,"title":"Research","hide_globally":false,"feed_count":1,"total_unread":0}
		]`),
		"/v1/feeds": []byte(`[
			{"id":21,"user_id":7,"title":"Alpha","site_url":"https://alpha.invalid","feed_url":"https://alpha.invalid/rss","category":{"id":11,"user_id":7,"title":"General"}},
			{"id":22,"user_id":7,"title":"Beta","site_url":"https://beta.invalid","feed_url":"https://beta.invalid/rss","category":{"id":12,"user_id":7,"title":"Research"}}
		]`),
		"/v1/entries?limit=100&status=unread": []byte(`{"total":1,"entries":[{"id":31,"user_id":7,"feed_id":22,"hash":"unread-hash","title":"Unread","status":"unread","starred":false}]}`),
		"/v1/entries?limit=100&status=read":   []byte(`{"total":1,"entries":[{"id":32,"user_id":7,"feed_id":21,"hash":"read-hash","title":"Read","status":"read","starred":true}]}`),
		"/v1/entries?limit=100&starred=true":  []byte(`{"total":1,"entries":[{"id":32,"user_id":7,"feed_id":21,"hash":"read-hash","title":"Read","status":"read","starred":true}]}`),
	}
}

func TestDecodeAPIObservationRequiresAuthenticatedDataAndReturnsPerUserCounts(t *testing.T) {
	got, err := decodeAPIObservation(apiObservationFixture(), "2.2.19")
	if err != nil {
		t.Fatalf("valid authenticated API data rejected: %v", err)
	}
	want := apiObservation{UserID: 7, Username: "synthetic-reader", Categories: 2, Feeds: 2, CategoryFeeds: 2, CategoryUnread: 1, UnreadTotal: 1, ReadTotal: 1, StarredTotal: 1}
	if got != want {
		t.Fatalf("observation = %#v, want %#v", got, want)
	}
}

func TestDecodeAPIObservationRejectsHealthOnlyAndWrongVersion(t *testing.T) {
	if _, err := decodeAPIObservation(map[string][]byte{"/v1/me": []byte(`{"status":"ok"}`)}, "2.2.19"); err == nil {
		t.Fatal("health-only response passed authenticated data checks")
	}
	responses := apiObservationFixture()
	responses["/v1/version"] = []byte(`{"version":"2.3.3"}`)
	if _, err := decodeAPIObservation(responses, "2.2.19"); err == nil {
		t.Fatal("wrong application version passed")
	}
}

func TestDecodeAPIObservationRejectsCrossUserOrInvalidListTotals(t *testing.T) {
	t.Run("foreign user", func(t *testing.T) {
		responses := apiObservationFixture()
		responses["/v1/feeds"] = []byte(`[{"id":21,"user_id":8,"title":"Alpha","site_url":"https://alpha.invalid","feed_url":"https://alpha.invalid/rss","category":{"id":11,"user_id":8,"title":"General"}}]`)
		if _, err := decodeAPIObservation(responses, "2.2.19"); err == nil {
			t.Fatal("feed owned by another user passed")
		}
	})
	t.Run("total smaller than returned entries", func(t *testing.T) {
		responses := apiObservationFixture()
		responses["/v1/entries?limit=100&status=unread"] = []byte(`{"total":0,"entries":[{"id":31,"user_id":7,"feed_id":22,"hash":"unread-hash","title":"Unread","status":"unread","starred":false}]}`)
		if _, err := decodeAPIObservation(responses, "2.2.19"); err == nil {
			t.Fatal("inconsistent API total passed")
		}
	})
}
