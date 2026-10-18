package config

import "testing"

// The push target reads the docker-build git form, keeps a URL's own colons, and refuses
// a branch git would refuse and a folder outside the repository.
func TestParsePushTarget(t *testing.T) {
	ok := map[string]PushTarget{
		"":       {},
		"origin": {Remote: "origin"},
		"https://github.com/Anemos-labs/random-builds.git#build/tallyhall:web-apps/tallyhall": {Remote: "https://github.com/Anemos-labs/random-builds.git", Branch: "build/tallyhall", Dir: "web-apps/tallyhall"},
		"git@github.com:o/r.git#:apps/x/": {Remote: "git@github.com:o/r.git", Dir: "apps/x"},
		"#main":                           {Branch: "main"},
		"origin#b:.":                      {Remote: "origin", Branch: "b"},
	}
	for in, want := range ok {
		got, err := ParsePushTarget(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
		if again, err := ParsePushTarget(got.String()); err != nil || again != got {
			t.Errorf("%q: String() %q reads back as %+v, %v", in, got.String(), again, err)
		}
	}
	for _, bad := range []string{"origin#a..b", "origin#-x", "origin#:../x", "origin#:/abs", "origin#:a/../../b", "origin#:x/.git/y", "-o", "origin#a b", "origin#.hidden"} {
		if _, err := ParsePushTarget(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}
