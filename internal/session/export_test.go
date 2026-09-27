package session

import "github.com/positronico/arcctl/internal/catalog"

// SetVerified gives w hardware records in place of the compiled ones, so a
// test can open the factory reset. No release code can reach it.
func SetVerified(w *Writes, vs catalog.Verifications) { w.verified = vs }
