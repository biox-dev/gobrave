package utils

import (
	"net/url"
	"path/filepath"
	"strings"
)

// BuildSQLiteDSN builds a "file:" URI data source name for SQLite.
//
// Raw path concat such as "/tmp/a.db?_pragma=..." only works on Unix: on
// Windows "?" is not a legal filename character, so the driver fails with
// "The filename, directory name, or volume label syntax is incorrect".
// Building a URI keeps Windows drive/UNC paths and connection options valid,
// and percent-encodes characters that are illegal in a URI.
func BuildSQLiteDSN(dbPath string, pragmas ...string) string {
	path := filepath.ToSlash(dbPath)
	if filepath.VolumeName(dbPath) != "" && !strings.HasPrefix(path, "/") {
		// C:/dir/db.db -> /C:/dir/db.db, so the drive is the URI path.
		path = "/" + path
	}

	u := url.URL{Scheme: "file", Path: path}
	if len(pragmas) > 0 {
		query := u.Query()
		for _, pragma := range pragmas {
			query.Add("_pragma", pragma)
		}
		u.RawQuery = query.Encode()
	}
	return u.String()
}
