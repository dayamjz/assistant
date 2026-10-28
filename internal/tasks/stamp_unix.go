//go:build linux || darwin

package tasks

import "golang.org/x/sys/unix"

// stamp is what the file system records of an entry, stampOf's.
type stamp struct {
	ino          uint64
	size         int64
	mtime, ctime [2]int64
}

// stampOf reads what the file system records of the entry at p, without
// following a link: its inode, size, and modification and change times,
// which readCopyState reads twice to see a change recorded there while an
// entry is read. A write that leaves them as they were is not seen.
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
