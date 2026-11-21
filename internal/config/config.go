// Package config reads the settings from environment variables and an optional .env file.
//
// The names and meanings are the same as in the Python version (config.py), so one .env serves both.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type Settings struct {
	BotToken    string
	OwnerID     int64
	DatabaseURL string

	LogLevel      string
	DevLogChat    int64 // 0 = not set
	DevLogThread  int64
	LogChatID     int64
	LogThreadID   int64
	CommandPrefix string // characters that can start a command

	// ChatCleanupHours is how often the stored chats are checked against Telegram. 0 turns the schedule off.
	ChatCleanupHours float64

	// LogReportHours is how often the log goes to the developer log as a .txt file. 0 turns the reports off.
	LogReportHours float64

	// HealthPort is the port of the health endpoint (GET /health) for monitoring. 0 = off.
	HealthPort int

	// APIID and APIHash (from my.telegram.org) turn on the MTProto client that lists the members of a chat for /zombies.
	APIID   int
	APIHash string

	LocalesDir string

	// SupportChatUsername is where banned people can appeal and what /start shows (@name, may be empty).
	SupportChatUsername string
	// GithubURL is the button in /start (private chat); empty for none.
	GithubURL string

	// TelegramAPIURL is the Bot API server (empty = api.telegram.org). For a local Bot API server and for tests.
	TelegramAPIURL string
}

// Load reads .env (if present, never overriding real environment variables) and then the environment.
func Load(envFile string) (*Settings, error) {
	file := map[string]string{}
	if f, err := os.Open(envFile); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
				value = value[1 : len(value)-1]
			}
			file[strings.TrimSpace(key)] = value
		}
	}
	get := func(key string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return strings.TrimSpace(file[key])
	}
	integer := func(key string) (int64, error) {
		v := get(key)
		if v == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be a number, got %q", key, v)
		}
		return n, nil
	}

	s := &Settings{
		BotToken:       get("BOT_TOKEN"),
		DatabaseURL:    get("DATABASE_URL"),
		LogLevel:       strings.ToUpper(get("LOG_LEVEL")),
		CommandPrefix:  get("COMMAND_PREFIXES"),
		LocalesDir:     get("LOCALES_DIR"),
		TelegramAPIURL: get("TELEGRAM_API_URL"),
	}
	s.SupportChatUsername = "@ArgoRobotSupport"
	if v, ok := os.LookupEnv("SUPPORT_CHAT_USERNAME"); ok {
		s.SupportChatUsername = strings.TrimSpace(v)
	} else if v, ok := file["SUPPORT_CHAT_USERNAME"]; ok {
		s.SupportChatUsername = strings.TrimSpace(v)
	}
	s.GithubURL = "https://github.com/R0Xofficial/ArgoRobot"
	if v, ok := os.LookupEnv("GITHUB_URL"); ok {
		s.GithubURL = strings.TrimSpace(v)
	} else if v, ok := file["GITHUB_URL"]; ok {
		s.GithubURL = strings.TrimSpace(v)
	}
	if s.LogLevel == "" {
		s.LogLevel = "INFO"
	}
	if s.CommandPrefix == "" {
		s.CommandPrefix = "/!"
	}
	if s.LocalesDir == "" {
		s.LocalesDir = "locales"
	}
	var errs []error
	s.ChatCleanupHours = 24
	if v := strings.TrimSpace(os.Getenv("CHAT_CLEANUP_HOURS")); v != "" {
		if h, err := strconv.ParseFloat(v, 64); err != nil || h < 0 {
			errs = append(errs, errors.New("CHAT_CLEANUP_HOURS must be a number of hours (0 = off)"))
		} else {
			s.ChatCleanupHours = h
		}
	}
	if v := strings.TrimSpace(os.Getenv("API_ID")); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			errs = append(errs, errors.New("API_ID must be a number (from my.telegram.org)"))
		} else {
			s.APIID = n
		}
	}
	s.APIHash = strings.TrimSpace(os.Getenv("API_HASH"))
	if v := strings.TrimSpace(os.Getenv("HEALTH_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 65535 {
			errs = append(errs, errors.New("HEALTH_PORT must be a port number (0 = off)"))
		} else {
			s.HealthPort = n
		}
	}
	s.LogReportHours = 6
	if v := strings.TrimSpace(os.Getenv("LOG_REPORT_HOURS")); v != "" {
		if h, err := strconv.ParseFloat(v, 64); err != nil || h < 0 {
			errs = append(errs, errors.New("LOG_REPORT_HOURS must be a number of hours (0 = off)"))
		} else {
			s.LogReportHours = h
		}
	}
	for _, item := range []struct {
		key string
		dst *int64
	}{
		{"OWNER_ID", &s.OwnerID}, {"DEV_LOG_CHAT", &s.DevLogChat}, {"DEV_LOG_THREAD_ID", &s.DevLogThread},
		{"LOG_CHAT_ID", &s.LogChatID}, {"LOG_THREAD_ID", &s.LogThreadID},
	} {
		n, err := integer(item.key)
		if err != nil {
			errs = append(errs, err)
		}
		*item.dst = n
	}
	if s.BotToken == "" {
		errs = append(errs, errors.New("BOT_TOKEN is required"))
	}
	if s.OwnerID == 0 {
		errs = append(errs, errors.New("OWNER_ID is required"))
	}
	if s.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if s.BotToken != "" && !strings.Contains(s.BotToken, ":") {
		errs = append(errs, errors.New("BOT_TOKEN does not look like a Telegram bot token"))
	}
	if s.DatabaseURL != "" && !strings.HasPrefix(s.DatabaseURL, "postgresql://") && !strings.HasPrefix(s.DatabaseURL, "postgres://") {
		errs = append(errs, errors.New("DATABASE_URL must start with postgresql://"))
	}
	for _, r := range s.CommandPrefix {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			errs = append(errs, errors.New("COMMAND_PREFIXES must contain only symbol characters, e.g. /!"))
			break
		}
	}
	if s.GithubURL != "" && !strings.HasPrefix(s.GithubURL, "https://") && !strings.HasPrefix(s.GithubURL, "http://") {
		errs = append(errs, errors.New("GITHUB_URL must start with https://"))
	}
	return s, errors.Join(errs...)
}
