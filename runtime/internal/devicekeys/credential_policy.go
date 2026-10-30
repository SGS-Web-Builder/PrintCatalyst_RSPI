package devicekeys

// Ordinary files stay owner-only; systemd may deliver read-only root:root files.
func credentialPermissions(mode, uid, gid, serviceUID uint32, protected bool) bool {
	if uid != 0 && uid != serviceUID {
		return false
	}
	if mode&0077 == 0 {
		return true
	}
	return protected && mode&07777 == 0440 && uid == 0 && gid == 0
}
