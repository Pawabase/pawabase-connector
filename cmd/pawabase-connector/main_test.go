package main

import "testing"

func TestProblemTextReadsEveryErrorShape(t *testing.T) {
	cases := map[string]string{
		`"this enrollment token is unknown or already used"`: "this enrollment token is unknown or already used",
		`{"detail": "expired"}`:                              "expired",
		`{"message": "nope", "detail": "x"}`:                 "nope",
		`{"error": "bad"}`:                                   "bad",
		`Bad Gateway`:                                        "Bad Gateway",
	}
	for raw, want := range cases {
		if got := problemText([]byte(raw)); got != want {
			t.Errorf("problemText(%s) = %q, want %q", raw, got, want)
		}
	}
}
