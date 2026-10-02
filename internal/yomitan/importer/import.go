package importer

import (
	"strings"

	"github.com/xythh/ann2html/internal/store"
)

// Import imports a dictionary archive (.zip) or a dictionary collection
// export (.json), picking the importer by file extension.
func Import(st *store.Store, path string, progress Progress) error {
	if strings.EqualFold(pathExt(path), ".zip") {
		return ImportZip(st, path, progress)
	}
	return ImportCollection(st, path, progress)
}

func pathExt(p string) string {
	i := strings.LastIndexAny(p, `./\`)
	if i < 0 || p[i] != '.' {
		return ""
	}
	return p[i:]
}
