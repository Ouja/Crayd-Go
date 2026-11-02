package crayd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearEnv makes sure keys are unset now and restored to their old state after the test.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "") // registers the restore
		os.Unsetenv(k)
	}
}

func TestParseDotEnv(t *testing.T) {
	src := "\uFEFF# comment\r\n" +
		"\r\n" +
		"A=1\r\n" +
		"export B=two words\n" +
		"  C  =  spaced  \n" +
		"D=\"quoted # not comment\"  # real comment\n" +
		"E='single \\n literal'\n" +
		"F=\"line1\\nline2\\t\\\"q\\\" back\\\\slash\"\n" +
		"G=value # trailing comment\n" +
		"H=no#comment\n" +
		"I=\n" +
		"J=a=b=c\n" +
		"K=\"C:\\data\\sub\"\n" +
		"K2='C:\\temp\\new'\n" +
		"K3=C:\\temp\\new\n" +
		"L=postgres://u:p%23w@localhost:5432/db?sslmode=disable\n" +
		"M=\"\"\n"
	pairs, err := parseDotEnv(src)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range pairs {
		got[kv[0]] = kv[1]
	}
	want := map[string]string{
		"A": "1", "B": "two words", "C": "spaced", "D": "quoted # not comment",
		"E": `single \n literal`, "F": "line1\nline2\t\"q\" back\\slash", "G": "value",
		"H": "no#comment", "I": "", "J": "a=b=c",
		"K":  `C:\data\sub`, // unknown escapes (\d, \s) are kept as written
		"K2": `C:\temp\new`, // single quotes: always literal, the safe way to write Windows paths
		"K3": `C:\temp\new`, // unquoted: literal too
		"L":  "postgres://u:p%23w@localhost:5432/db?sslmode=disable", "M": "",
	}
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("%s = %q (present=%v), want %q", k, g, ok, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d keys, want %d", len(got), len(want))
	}
}

func TestParseDotEnvErrors(t *testing.T) {
	for src, want := range map[string]string{
		"just words":              "line 1: expected KEY=value",
		"=value":                  "line 1: expected KEY=value",
		"A=1\n1BAD=x":             "line 2",
		"BAD KEY=x":               "not a valid variable name",
		"A=\"unterminated":        "unterminated double quote",
		"A='unterminated":         "unterminated single quote",
		"A=\"x\" trailing":        "unexpected text after the closing quote",
		"SECRET=\"hunter2\" junk": "line 1 (SECRET)",
	} {
		_, err := parseDotEnv(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", src, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks the secret value: %v", err)
		}
	}
}

func TestLoadDotEnv(t *testing.T) {
	clearEnv(t, "CRAYD_T_NEW", "CRAYD_T_KEEP", "CRAYD_T_EMPTY")
	t.Setenv("CRAYD_T_KEEP", "from-real-env")
	t.Setenv("CRAYD_T_EMPTY", "") // set but empty still counts as set

	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("CRAYD_T_NEW=from-file\nCRAYD_T_KEEP=from-file\nCRAYD_T_EMPTY=from-file\n"), 0o600)
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CRAYD_T_NEW") != "from-file" {
		t.Error("new variable not loaded")
	}
	if os.Getenv("CRAYD_T_KEEP") != "from-real-env" {
		t.Error("the real environment must win over the file")
	}
	if v, _ := os.LookupEnv("CRAYD_T_EMPTY"); v != "" {
		t.Error("an explicitly empty variable must win over the file")
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Errorf("missing file should be fine: %v", err)
	}
}

func TestLoadDotEnvIsAllOrNothing(t *testing.T) {
	clearEnv(t, "CRAYD_T_FIRST")
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("CRAYD_T_FIRST=1\nthis line is broken\n"), 0o600)
	err := LoadDotEnv(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
	if _, set := os.LookupEnv("CRAYD_T_FIRST"); set {
		t.Error("a broken file must not be half applied")
	}
}

func TestEnvHelpers(t *testing.T) {
	clearEnv(t, "T_UNSET")
	t.Setenv("T_STR", "  hello  ")
	t.Setenv("T_BLANK", "   ")
	t.Setenv("T_INT", "42")
	t.Setenv("T_BOOLS", "YES")
	t.Setenv("T_DUR", "1m30s")
	t.Setenv("T_ONE", "PRODUCTION")

	e := &Env{}
	if e.String("T_STR", "d") != "hello" || e.String("T_BLANK", "d") != "d" || e.String("T_UNSET", "d") != "d" {
		t.Error("String")
	}
	if e.Int("T_INT", 0) != 42 || e.Int("T_UNSET", 7) != 7 {
		t.Error("Int")
	}
	if !e.Bool("T_BOOLS", false) || e.Bool("T_UNSET", true) != true {
		t.Error("Bool")
	}
	if e.Duration("T_DUR", 0) != 90*time.Second || e.Duration("T_UNSET", time.Second) != time.Second {
		t.Error("Duration")
	}
	if e.OneOf("T_ONE", "development", "development", "production") != "production" {
		t.Error("OneOf")
	}
	if e.Required("T_STR") != "hello" {
		t.Error("Required")
	}
	if err := e.Err(); err != nil {
		t.Fatalf("unexpected problems: %v", err)
	}
}

func TestEnvCollectsAllProblems(t *testing.T) {
	clearEnv(t, "T_NEEDED")
	t.Setenv("T_SECRET", "hunter2-do-not-print")
	t.Setenv("T_INT", "abc")
	t.Setenv("T_BOOL", "maybe")
	t.Setenv("T_DUR", "-5s")
	t.Setenv("T_ONE", "staging")

	e := &Env{}
	e.Required("T_NEEDED")
	e.Required("T_SECRET")
	if e.Int("T_INT", 5) != 5 || e.Bool("T_BOOL", true) != true || e.Duration("T_DUR", time.Minute) != time.Minute ||
		e.OneOf("T_ONE", "development", "development", "production") != "development" {
		t.Error("defaults must be returned for invalid values")
	}
	e.Add("custom problem")
	err := e.Err()
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	for _, want := range []string{"T_NEEDED is required", "T_INT must be a whole number", `"abc"`, "T_BOOL must be true or false",
		"T_DUR must be a duration", "T_ONE must be one of development, production", "custom problem"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "hunter2") {
		t.Errorf("secret leaked:\n%s", msg)
	}
	if strings.Count(msg, "\n  - ") != 6 {
		t.Errorf("want 6 listed problems:\n%s", msg)
	}
}
