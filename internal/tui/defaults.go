package tui

import "github.com/positronico/arcctl/internal/session"

// defaultTabs are the tabs of a real run over api, in their bar order.
func defaultTabs(api session.API) []Tab {
	return []Tab{NewButtons(api.Read), NewDPITab(), NewInfoTab(), NewLogTab()}
}
