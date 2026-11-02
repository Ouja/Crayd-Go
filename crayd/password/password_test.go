package password

import (
	"strings"
	"testing"
)

func init() { Iterations = 1000 } // keep tests fast; production uses 600,000

func TestHashAndCheck(t *testing.T) {
	h, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$1000$") {
		t.Fatalf("unexpected format %q", h)
	}
	if !Check(h, "correct horse") {
		t.Fatal("right password rejected")
	}
	for _, wrong := range []string{"", "correct horse ", "Correct horse", "correct hors"} {
		if Check(h, wrong) {
			t.Fatalf("wrong password %q accepted", wrong)
		}
	}
	h2, _ := Hash("correct horse")
	if h == h2 {
		t.Fatal("same password produced the same hash: salt missing")
	}
}

func TestMalformedHashesNeverMatch(t *testing.T) {
	good, _ := Hash("pw")
	parts := strings.Split(good, "$")
	for _, h := range []string{
		"", "pw", "$$$", "bcrypt$1$aaaa$bbbb",
		"pbkdf2-sha256$0$" + parts[2] + "$" + parts[3],           // zero iterations
		"pbkdf2-sha256$-5$" + parts[2] + "$" + parts[3],          // negative
		"pbkdf2-sha256$99999999999$" + parts[2] + "$" + parts[3], // absurd (DoS)
		"pbkdf2-sha256$1000$!!$" + parts[3],                      // bad base64
		"pbkdf2-sha256$1000$" + parts[2] + "$",                   // empty key
		good + "$extra",
	} {
		if Check(h, "pw") {
			t.Errorf("malformed hash %q matched", h)
		}
		if !NeedsRehash(h) {
			t.Errorf("malformed hash %q should need rehash", h)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	h, _ := Hash("pw")
	if NeedsRehash(h) {
		t.Fatal("fresh hash needs rehash")
	}
	old := Iterations
	Iterations = 2000
	defer func() { Iterations = old }()
	if !NeedsRehash(h) {
		t.Fatal("weaker hash should need rehash")
	}
	if !Check(h, "pw") {
		t.Fatal("old hashes must keep working after raising Iterations")
	}
}

func TestLengthLimit(t *testing.T) {
	long := strings.Repeat("a", 1025)
	if _, err := Hash(long); err != ErrTooLong {
		t.Fatalf("err = %v", err)
	}
	h, _ := Hash(strings.Repeat("a", 1024))
	if !Check(h, strings.Repeat("a", 1024)) || Check(h, long) {
		t.Fatal("length limit handling wrong")
	}
}
