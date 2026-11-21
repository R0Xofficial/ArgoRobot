package modules

import (
	"errors"
	"strings"

	"argo/internal/core"
)

func asModeration(err error, target **core.ModerationError) bool { return errors.As(err, target) }

func trimSpace(s string) string { return strings.TrimSpace(s) }
