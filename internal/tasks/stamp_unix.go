//go:build linux || darwin

package tasks

import "golang.org/x/sys/unix"

// stampOf reads what the file system records of the entry at p, without
// following a link: its inode, size, and modification and change times,
// which readCopyState reads twice to see an entry change while it is read.
func stampOf(p string) (stamp, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return stamp{}, err
	}
	s := stamp{ino: st.Ino, size: st.Size}
	s.mtime[0], s.mtime[1] = st.Mtim.Unix()
	s.ctime[0], s.ctime[1] = st.Ctim.Unix()
	return s, nil
}
