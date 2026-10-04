package consume

import "testing"

func TestKeyForJoinsThePrefixAndTheLeadingSlashedPath(t *testing.T) {
	cases := map[string]string{
		KeyFor("weeb", StoredEvent{Path: "/posters/abc"}):   "weeb/posters/abc",
		KeyFor("weeb-staging/", StoredEvent{Path: "/abc"}):  "weeb-staging/abc",
		KeyFor("weeb", StoredEvent{Path: "characters/abc"}): "weeb/characters/abc",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
