// Package modules holds the commands of the bot, one file per module, as in the Python version's modules/.
package modules

import (
	"argo/internal/core"
	"argo/internal/i18n"
)

// Register adds every ported module to the router.
func Register(app *core.App) {
	r := app.Router
	registerBans(r)
	registerWarns(r)
	registerLanguage(r)
}

// text detail for a log entry: both the label and the value are tags, written in the log chat's language.
func tagDetail(labelTag, valueTag string) core.Detail {
	return func(l i18n.Lang) (string, string) { return l.T(labelTag), l.T(valueTag) }
}

func valueDetail(labelTag, value string) core.Detail {
	return func(l i18n.Lang) (string, string) { return l.T(labelTag), value }
}
