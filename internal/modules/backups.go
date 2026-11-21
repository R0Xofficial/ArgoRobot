package modules

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"argo/internal/core"
	"argo/internal/text"
)

// Backups: save a chat's settings and data to a file, and put them back.

const backupCooldown = 20 * time.Second

func registerBackups(r *core.Router) {
	r.Command(cmdExportitems, "exportitems")
	r.Command(cmdExport, "export")
	r.Command(cmdImport, "import")
}

func backupWords(args string) []string {
	return strings.Fields(strings.ReplaceAll(strings.ToLower(args), ",", " "))
}

func backupSummary(counts [][2]any) string {
	parts := make([]string, len(counts))
	for i, c := range counts {
		parts[i] = fmt.Sprintf("%s: %d", c[0], c[1])
	}
	return strings.Join(parts, ", ")
}

func sayBackupError(c *core.Ctx, err error) (bool, error) {
	var be *core.BackupError
	if errors.As(err, &be) {
		c.Say(text.Esc(be.Msg))
		return true, nil
	}
	return false, err
}

func cmdExportitems(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	lines := []string{c.T("backups.items_of_a_backup")}
	for _, it := range core.BackupItems {
		lines = append(lines, "• <code>"+it.Key+"</code> - "+text.Esc(it.Label))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdExport(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	items, err := core.ResolveBackupItems(backupWords(c.Args))
	if err != nil {
		_, err = sayBackupError(c, err)
		return err
	}
	chat := c.Msg.Chat
	key := fmt.Sprintf("export:%d", chat.Id)
	if !c.App.Cooldown(key, backupCooldown) {
		c.Say(c.T("common.wait_a_little_before_exporting_again"))
		return nil
	}
	data, counts, err := c.App.ExportBackup(c.Ctx, chat.Id, chat.Title, items)
	if err != nil {
		c.App.ForgetCooldown(key)
		return err
	}
	if len(counts) == 0 {
		c.App.ForgetCooldown(key)
		c.Say(c.T("backups.there_is_nothing_to_save_yet"))
		return nil
	}
	name := fmt.Sprintf("ArgoBackup_%d", abs64(chat.Id))
	if len(items) == 1 {
		name += "_" + items[0].Key
	}
	title := chat.Title
	if title == "" {
		title = c.T("common.a_chat")
	}
	summary := backupSummary(counts)
	if err := c.SendDocumentTo(c.User.Id, name+".json", data, c.T("backups.backup_of", core.V{"v": text.Esc(title), "summary": summary})); err != nil {
		c.App.ForgetCooldown(key)
		c.Say(c.T("backups.i_can_t_message_you_in"))
		return nil
	}
	c.Say(c.T("backups.the_backup_was_sent_to_you", core.V{"summary": summary}))
	return nil
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func cmdImport(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	words := backupWords(c.Args)
	overwrite := false
	var rest []string
	for _, w := range words {
		switch w {
		case "overwrite":
			overwrite = true
		case "keep":
		default:
			rest = append(rest, w)
		}
	}
	words = rest
	wanted, err := core.ResolveBackupItems(words)
	if err != nil {
		_, err = sayBackupError(c, err)
		return err
	}
	doc := c.Msg.Document
	if doc == nil {
		if replied := core.RepliedMessage(c.Msg); replied != nil {
			doc = replied.Document
		}
	}
	if doc == nil {
		c.Say(c.T("backups.reply_to_a_backup_file_or"))
		return nil
	}
	if doc.FileSize > core.MaxBackupFileBytes {
		c.Say(c.T("common.that_file_is_too_big_the"))
		return nil
	}
	raw, err := c.Download(doc.FileId)
	if err != nil {
		c.Say(c.T("common.i_couldn_t_download_that_file"))
		return nil
	}
	content, err := core.ReadBackup(raw)
	if err != nil {
		_, err = sayBackupError(c, err)
		return err
	}
	result, err := c.App.ImportBackup(c.Ctx, c.Msg.Chat.Id, content, wanted, overwrite)
	if err != nil {
		slog.Error("Importing a backup failed", "err", err)
		c.Say(c.T("backups.the_import_failed_halfway_run_it"))
		return err
	}
	if len(result) == 0 {
		c.Say(c.T("backups.the_file_has_none_of_those"))
		return nil
	}
	head := c.T("backups.backup_restored")
	if overwrite {
		head = c.T("backups.backup_restored_replacing")
	}
	lines := []string{head}
	done := map[string]bool{}
	for _, r := range result {
		done[r.Key] = true
		var msg string
		if overwrite {
			msg = c.T("backups.written", core.V{"count": r.Counts.Added})
		} else {
			msg = c.T("backups.added", core.V{"count": r.Counts.Added})
		}
		if r.Counts.Kept > 0 {
			msg += c.T("backups.already_there", core.V{"kept": r.Counts.Kept})
		}
		if r.Counts.Skipped > 0 {
			msg += c.T("backups.skipped", core.V{"skipped": r.Counts.Skipped})
		}
		label := r.Key
		for _, it := range core.BackupItems {
			if it.Key == r.Key {
				label = it.Label
			}
		}
		lines = append(lines, "• "+text.Esc(label)+": "+msg)
	}
	var missing []string
	for _, it := range wanted {
		if !done[it.Key] {
			missing = append(missing, it.Label)
		}
	}
	if len(missing) > 0 && len(words) > 0 {
		lines = append(lines, c.T("backups.not_in_the_file")+text.Esc(strings.Join(missing, ", "))+".")
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}
