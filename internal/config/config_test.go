package config

import (
	"os"
	"path/filepath"
	"testing"
)

var allKeys = []string{"BOT_TOKEN", "OWNER_ID", "DATABASE_URL", "API_ID", "API_HASH", "LOG_REPORT_HOURS",
	"CHAT_CLEANUP_HOURS", "DEV_LOG_CHAT", "DEV_LOG_THREAD_ID", "LOG_CHAT_ID", "LOG_THREAD_ID", "LOG_LEVEL",
	"COMMAND_PREFIXES", "LOCALES_DIR", "TELEGRAM_API_URL", "SUPPORT_CHAT_USERNAME", "GITHUB_URL"}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allKeys {
		t.Setenv(k, "") // restored afterwards; an empty value counts as not set
	}
}

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = "BOT_TOKEN=1:abc\nOWNER_ID=5\nDATABASE_URL=postgres://x\n"

func TestDefaults(t *testing.T) {
	clearEnv(t)
	s, err := Load(writeEnv(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if s.LogReportHours != 6 || s.ChatCleanupHours != 24 || s.APIID != 0 || s.APIHash != "" {
		t.Errorf("%+v", s)
	}
}

// Every setting can be given in the .env file, not only in the real environment.
func TestAllSettingsFromTheEnvFile(t *testing.T) {
	clearEnv(t)
	s, err := Load(writeEnv(t, minimal+"API_ID=12345\nAPI_HASH=\"abcdef\"\nLOG_REPORT_HOURS=2.5\n"+
		"CHAT_CLEANUP_HOURS=12\nDEV_LOG_CHAT=-100123\nDEV_LOG_THREAD_ID=7\nLOG_LEVEL=debug\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.APIID != 12345 || s.APIHash != "abcdef" || s.LogReportHours != 2.5 ||
		s.ChatCleanupHours != 12 || s.DevLogChat != -100123 || s.DevLogThread != 7 || s.LogLevel != "DEBUG" {
		t.Errorf("%+v", s)
	}
}

func TestGithubURLEmptyMeansDefault(t *testing.T) {
	clearEnv(t)
	s, err := Load(writeEnv(t, minimal+"GITHUB_URL=\n"))
	if err != nil || s.GithubURL != "https://github.com/R0Xofficial/ArgoRobot" {
		t.Fatalf("%q %v", s.GithubURL, err)
	}
	s, err = Load(writeEnv(t, minimal+"GITHUB_URL=https://example.com/x\n"))
	if err != nil || s.GithubURL != "https://example.com/x" {
		t.Fatalf("%q %v", s.GithubURL, err)
	}
}

func TestEnvironmentBeatsTheFileAndBadValuesAreReported(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOG_REPORT_HOURS", "9")
	s, err := Load(writeEnv(t, minimal+"LOG_REPORT_HOURS=3\n"))
	if err != nil || s.LogReportHours != 9 {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range []string{"API_ID=abc", "LOG_REPORT_HOURS=-1", "CHAT_CLEANUP_HOURS=x"} {
		clearEnv(t)
		if _, err := Load(writeEnv(t, minimal+bad+"\n")); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
