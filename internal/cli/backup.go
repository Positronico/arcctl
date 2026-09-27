package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/session"
)

func runBackup(r *runner, args []string) error {
	fs := r.flagSet("backup", synopsis("backup"))
	full := fs.Bool("full", false, "first read every backup range still unknown (about 725 reads for a mouse)")
	out := fs.String("o", "", "write to `file`, which must not exist, instead of the backups folder")
	label := fs.String("label", "", "`text` kept in the backup and its file name")
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	c, sn, err := r.connect(loaded)
	if err != nil {
		return err
	}
	defer c.stop()
	if err := loadable(sn); err != nil {
		return err
	}
	if c.w.source != backup.SourceDevice && *out == "" {
		return usageError("backup: with --emulate or --replay, pass -o so the backup stays out of the backups folder")
	}
	var cp session.Capture
	err = r.do(c, func(ctx context.Context) error {
		var err error
		cp, err = c.s.Backup(ctx, *full)
		return err
	})
	if err != nil {
		return readError(err)
	}
	meta := backup.Meta{Tool: "arcctl " + r.env.Version, Source: c.w.source, Label: *label, Created: r.env.Now(), OS: r.keyOS()}
	if h := c.s.Snapshot().Handshake; h != nil {
		conn := h.Conn
		meta.Conn = &conn
	}
	f, err := backup.New(cp, meta)
	if err != nil {
		return err
	}
	path := *out
	if path != "" {
		err = backup.SaveAs(path, f)
	} else {
		paths, perr := r.env.Paths()
		if perr != nil {
			return perr
		}
		path, err = backup.Save(paths.Backups, f)
	}
	if err != nil {
		return err
	}
	kind := "the loaded bytes"
	if f.Full {
		kind = "full"
	}
	name := "unknown model"
	if f.Model != nil {
		name = f.Model.Name
	}
	fmt.Fprintf(r.out, "Saved %s\n  %s, identity %s, %d bytes (%s)\n", path, name, f.Key(), f.Known(), kind)
	if miss := f.Missing(); len(miss) > 0 {
		words := make([]string, len(miss))
		for i, e := range miss {
			words[i] = e.String()
		}
		fmt.Fprintln(r.errw, wrap("arcctl: the backup is partial; these ranges could not be read: ", "  ", words, ", ", 80))
	}
	return nil
}

func runBackups(r *runner, args []string) error {
	fs := r.flagSet("backups", synopsis("backups"))
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	paths, err := r.env.Paths()
	if err != nil {
		return err
	}
	list, err := backup.List(paths.Backups)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(r.out, "No backups in %s\n", paths.Backups)
		return nil
	}
	var rows [][]string
	for _, l := range list {
		if l.Err != nil {
			rows = append(rows, []string{"unreadable", l.Err.Error()})
			continue
		}
		f := l.File
		kind := "loaded"
		if f.Full {
			kind = "full"
		}
		if len(f.Missing()) > 0 {
			kind += ", partial"
		}
		model := "unknown"
		if f.Model != nil {
			model = f.Model.Key
		}
		rows = append(rows, []string{f.Created.UTC().Format(time.DateTime), model, f.Key(), kind, f.Label})
		rows = append(rows, []string{"  " + l.Path})
	}
	table(r.out, "", rows)
	return nil
}
