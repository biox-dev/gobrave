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
	volume := filepath.VolumeName(dbPath)
	path := filepath.ToSlash(dbPath)

	var dsn string
	if len(volume) == 2 && volume[1] == ':' {
		// Windows drive path. The VFS used by go-sqlite3 does not understand
		// the leading "/" of "file:///C:/dir/db.db" and tries to open the
		// bogus filename "/C:", so the drive must follow the scheme directly:
		// "file:C:/dir/db.db".
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath()
	} else {
		if volume != "" && !strings.HasPrefix(path, "/") {
			// C:/dir/db.db -> /C:/dir/db.db, so the drive is the URI path.
			path = "/" + path
		}
		dsn = (&url.URL{Scheme: "file", Path: path}).String()
	}

	if len(pragmas) == 0 {
		return dsn
	}

	query := url.Values{}
	for _, pragma := range pragmas {
		query.Add("_pragma", pragma)
	}
	return dsn + "?" + query.Encode()
}
