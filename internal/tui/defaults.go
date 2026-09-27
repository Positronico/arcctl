package tui

import "github.com/positronico/arcctl/internal/library"

// defaultTabs are the tabs of a real run over o.Session, in their bar order
// (§7.2). The Macros tab keeps its library at o.Library; without one it
// works on the mouse's macros alone.
func defaultTabs(o Options) []Tab {
	api := o.Session
	var lib *library.Store
	if o.Library != "" {
		lib = &library.Store{Path: o.Library}
	}
	macros := NewMacros(api.Read, lib)
	return []Tab{NewButtons(api.Read, macros), NewDPITab(), macros, NewBackupTab(api, o.Backups, o.Source), NewInfoTab(), NewLogTab()}
}
