package core

import (
	"strconv"

	"argo/internal/i18n"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// V is shorthand for the values of a text.
type V = i18n.V

type i18nV = i18n.V
