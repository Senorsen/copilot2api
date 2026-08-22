package securetoken

import "testing"

func TestDigestMatches(t *testing.T) {
	digest := Hash("correct-secret")
	for _, test := range []struct {
		name      string
		candidate string
		want      bool
	}{
		{name: "same token", candidate: "correct-secret", want: true},
		{name: "same length", candidate: "wrong---secret", want: false},
		{name: "different length", candidate: "wrong", want: false},
		{name: "empty", candidate: "", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := digest.Matches(test.candidate); got != test.want {
				t.Fatalf("Matches() = %v, want %v", got, test.want)
			}
		})
	}
}
