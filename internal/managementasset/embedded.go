package managementasset

import _ "embed"

//go:embed static/management.html
var embeddedManagementHTML []byte

// EmbeddedManagementHTML returns the bundled management panel asset.
func EmbeddedManagementHTML() ([]byte, bool) {
	if len(embeddedManagementHTML) == 0 {
		return nil, false
	}
	return embeddedManagementHTML, true
}

// HasEmbeddedManagementHTML reports whether the binary includes a bundled panel asset.
func HasEmbeddedManagementHTML() bool {
	return len(embeddedManagementHTML) > 0
}
