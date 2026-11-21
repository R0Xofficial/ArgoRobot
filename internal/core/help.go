package core

// What a module declares for /help. The data is in modules/helpdata.go;
// the logic that shows it is in modules/misc.go.

// HelpEntry is one command of a module's help page.
type HelpEntry struct {
	Usage       string
	Description string // an i18n tag
	Label       string // an i18n tag: who may use it
	Section     string // an i18n tag; a module with sections shows one button per section
	MinRole     int    // 0 for everyone; the command is hidden from people below this role
}

// HelpNote is an extra block of a module's page.
type HelpNote struct{ Title, Text, Section string }

// Help is the help page of a module.
type Help struct {
	Title   string // an i18n tag, when the capitalized name would look wrong
	About   string // an i18n tag
	Who     string // the label given to every entry that has none
	Entries []HelpEntry
	Notes   []HelpNote
}

// HelpPage is reference text that is too long for the page and gets a button of its own; it returns one or more
// messages.
type HelpPage struct {
	Label string // an i18n tag
	Text  func(c *Ctx) []string
}
