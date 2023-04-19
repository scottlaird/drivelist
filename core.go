package drivelist

func GetAllDisks() (*Disks, error) {
	disks, err := GetDisks()
	if err != nil {
		return disks, err
	}

	err = AnnotateDisksZFS(disks)
	if err != nil {
		return disks, err
	}
	err = AnnotateDisksMounts(disks)
	if err != nil {
		return disks, err
	}
	err = AnnotateDisksMD(disks)
	if err != nil {
		return disks, err
	}
	err = AnnotateDisksLVM(disks)
	if err != nil {
		return disks, err
	}

	err = AnnotateEmptyBays(disks)
	if err != nil {
		return disks, err
	}

	return disks, nil
}
